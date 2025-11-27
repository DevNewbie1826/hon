package adaptor

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"

	"github.com/DevNewbie1826/hon/pkg/appcontext"
)

// ResponseWriter implements http.ResponseWriter and wraps netpoll connection.
// ResponseWriter는 http.ResponseWriter 인터페이스를 구현하며 netpoll 연결을 래핑합니다.
type ResponseWriter struct {
	ctx          *appcontext.RequestContext
	req          *http.Request
	header       http.Header
	statusCode   int
	hijacked     bool
	headerSent   bool
	chunked      bool
	bufWriter    *bufio.Writer // Buffering for efficient writes
}

// rwPool recycles ResponseWriter objects to reduce GC pressure.
// rwPool은 가비지 컬렉션(GC) 부하를 줄이기 위해 ResponseWriter 객체를 재활용합니다.
var rwPool = sync.Pool{
	New: func() any {
		return &ResponseWriter{
			header: make(http.Header),
		}
	},
}

// copyBufPool provides buffers for io.CopyBuffer to enable Zero-Alloc copying.
// copyBufPool은 io.CopyBuffer를 위한 버퍼를 제공하여 Zero-Alloc 복사를 가능하게 합니다.
var copyBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 32*1024) // 32KB buffer
		return &b
	},
}

// bufWriterPool recycles bufio.Writer objects.
// bufWriterPool은 bufio.Writer 객체를 재활용합니다.
var bufWriterPool = sync.Pool{
	New: func() any {
		// Default bufio.Writer buffer size is 4KB.
		// 기본 bufio.Writer 버퍼 크기는 4KB입니다.
		return bufio.NewWriter(nil) 
	},
}


// NewResponseWriter retrieves a ResponseWriter from the pool and initializes it.
// NewResponseWriter는 풀에서 ResponseWriter를 가져와 초기화합니다.
func NewResponseWriter(ctx *appcontext.RequestContext, req *http.Request) *ResponseWriter {
	w := rwPool.Get().(*ResponseWriter)
	w.ctx = ctx
	w.req = req
	w.statusCode = http.StatusOK
	w.hijacked = false
	w.headerSent = false
	w.chunked = false
	
	// Get bufio.Writer from pool and reset it with the current connection
	// 풀에서 bufio.Writer를 가져와 현재 연결로 리셋합니다.
	bw := bufWriterPool.Get().(*bufio.Writer)
	bw.Reset(w.ctx.Conn())
	w.bufWriter = bw

	return w
}

// Release returns the ResponseWriter and its resources to their respective pools.
// Release는 ResponseWriter와 리소스를 각각의 풀로 반환합니다.
func (w *ResponseWriter) Release() {
	// Put bufWriter back to pool FIRST
	// bufWriter를 먼저 풀에 반환합니다.
	if w.bufWriter != nil {
		bufWriterPool.Put(w.bufWriter)
		w.bufWriter = nil // Avoid lingering pointer
	}

	w.ctx = nil
	w.req = nil
	w.statusCode = 0
	w.hijacked = false
	w.headerSent = false
	w.chunked = false

	// Clear headers
	// 헤더 초기화
	for k := range w.header {
		delete(w.header, k)
	}

	rwPool.Put(w)
}

// GetRequest parses the HTTP request from the netpoll connection.
// GetRequest는 netpoll 연결에서 HTTP 요청을 파싱합니다.
func GetRequest(ctx *appcontext.RequestContext) (*http.Request, error) {
	// http.ReadRequest reads from the bufio.Reader wrapped around the netpoll connection
	// http.ReadRequest는 netpoll 연결을 감싼 bufio.Reader에서 읽어옵니다.
	req, err := http.ReadRequest(ctx.GetReader())
	if err != nil {
		return nil, err
	}

	// Set RemoteAddr
	// 원격 주소 설정
	if addr := ctx.Conn().RemoteAddr(); addr != nil {
		req.RemoteAddr = addr.String()
	}

	// Inherit context from the RequestContext (which manages timeouts and cancellation)
	// RequestContext(타임아웃 및 취소 관리)에서 컨텍스트를 상속받습니다.
	req = req.WithContext(ctx.Req())

	return req, nil
}

// Header returns the header map that will be sent by WriteHeader.
// Header는 WriteHeader에 의해 전송될 헤더 맵을 반환합니다.
func (w *ResponseWriter) Header() http.Header {
	return w.header
}

// Write writes the data to the connection as part of an HTTP reply.
// Write는 HTTP 응답의 일부로 데이터를 연결에 씁니다.
func (w *ResponseWriter) Write(p []byte) (int, error) {
	if w.hijacked {
		return 0, http.ErrHijacked
	}

	if !w.headerSent {
		if err := w.ensureHeaderSent(); err != nil {
			return 0, err
		}
	}

	if w.chunked {
		// Chunk header
		fmt.Fprintf(w.bufWriter, "%x\r\n", len(p))
		// Chunk data
		n, err := w.bufWriter.Write(p)
		// Chunk trailer
		w.bufWriter.Write([]byte("\r\n"))
		return n, err
	}

	return w.bufWriter.Write(p)
}

// ReadFrom implements io.ReaderFrom.
// ReadFrom은 io.ReaderFrom 인터페이스를 구현합니다.
// It uses copyBufPool to read data and writes directly to bufWriter.
// copyBufPool을 사용하여 데이터를 읽고 bufWriter에 직접 씀으로써 메모리 사용량과 복사를 최소화합니다.
func (w *ResponseWriter) ReadFrom(r io.Reader) (n int64, err error) {
	if w.hijacked {
		return 0, http.ErrHijacked
	}

	if !w.headerSent {
		if err := w.ensureHeaderSent(); err != nil {
			return 0, err
		}
	}

	// Future-proof: If netpoll supports io.ReaderFrom (sendfile), use it.
	// This requires that we are NOT using chunked encoding, as sendfile sends raw data.
	// 미래 대비: netpoll이 io.ReaderFrom(sendfile)을 지원하는 경우 이를 사용합니다.
	// sendfile은 원시 데이터를 전송하므로 Chunked 인코딩을 사용하지 않아야 합니다.
	if !w.chunked {
		// Flush any buffered data first to maintain order
		// 순서를 유지하기 위해 버퍼링된 데이터를 먼저 플러시합니다.
		w.bufWriter.Flush()

		// Check if the underlying connection implements io.ReaderFrom
		if rf, ok := w.ctx.Conn().(io.ReaderFrom); ok {
			return rf.ReadFrom(r)
		}
	}

	bufPtr := copyBufPool.Get().(*[]byte)
	buf := *bufPtr
	defer copyBufPool.Put(bufPtr)

	for {
		nr, er := r.Read(buf)
		if nr > 0 {
			// Write directly to bufWriter
			// bufWriter에 직접 씁니다.
			if w.chunked {
				// Chunk header
				fmt.Fprintf(w.bufWriter, "%x\r\n", nr)
				// Chunk data
				if _, ew := w.bufWriter.Write(buf[:nr]); ew != nil {
					err = ew
					break
				}
				// Chunk trailer
				if _, ew := w.bufWriter.Write([]byte("\r\n")); ew != nil {
					err = ew
					break
				}
			} else {
				// Normal direct write
				if _, ew := w.bufWriter.Write(buf[:nr]); ew != nil {
					err = ew
					break
				}
			}
			n += int64(nr)
		}
		if er != nil {
			if er != io.EOF {
				err = er
			}
			break
		}
	}
	return n, err
}

// WriteHeader sends an HTTP response header with the provided status code.
// WriteHeader는 제공된 상태 코드로 HTTP 응답 헤더를 전송합니다.
func (w *ResponseWriter) WriteHeader(statusCode int) {
	if w.hijacked || w.headerSent {
		return
	}
	w.statusCode = statusCode
}

// ensureHeaderSent sends headers if they haven't been sent yet.
// ensureHeaderSent는 헤더가 아직 전송되지 않았다면 전송합니다.
func (w *ResponseWriter) ensureHeaderSent() error {
	if w.headerSent {
		return nil
	}

	// If Content-Length is not set, we must use chunked encoding because we are streaming.
	// Content-Length가 설정되지 않았다면 스트리밍 중이므로 Chunked 인코딩을 사용해야 합니다.
	if w.header.Get("Content-Length") == "" {
		w.chunked = true
		w.header.Set("Transfer-Encoding", "chunked")
	} else {
		w.chunked = false
	}
	
	// Set Default Content-Type if not present
	// Content-Type이 없으면 기본값(text/plain 또는 application/octet-stream)을 추론하기 어렵습니다.
	// 표준 라이브러리는 Sniffing을 하지만 여기서는 생략하거나 기본값만 처리합니다.
	// 여기서는 생략합니다. 사용자가 설정해야 합니다.

	statusText := http.StatusText(w.statusCode)
	if statusText == "" {
		statusText = "status code " + fmt.Sprintf("%d", w.statusCode)
	}
	headerLine := fmt.Sprintf("HTTP/1.1 %d %s\r\n", w.statusCode, statusText)

	if _, err := w.bufWriter.Write([]byte(headerLine)); err != nil {
		return err
	}
	if err := w.header.Write(w.bufWriter); err != nil {
		return err
	}
	if _, err := w.bufWriter.Write([]byte("\r\n")); err != nil {
		return err
	}
	w.headerSent = true
	return nil
}

// Flush sends any buffered data to the client.
// Flush는 버퍼링된 모든 데이터를 클라이언트로 전송합니다.
// This will flush the underlying bufio.Writer.
// 이는 기반의 bufio.Writer를 플러시합니다.
func (w *ResponseWriter) Flush() {
	if w.hijacked {
		return
	}
	w.ensureHeaderSent()
	w.bufWriter.Flush()
}

// Hijack lets the caller take over the connection.
// Hijack은 호출자가 연결 제어권을 가져가도록 합니다.
func (w *ResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.hijacked {
		return nil, nil, errors.New("already hijacked")
	}
	w.hijacked = true

	// Ensure any buffered data is flushed before hijacking.
	// 하이재킹 전에 버퍼링된 데이터가 모두 플러시되었는지 확인합니다.
	w.bufWriter.Flush()

	conn := w.ctx.Conn()
	reader := w.ctx.GetReader()
	// Create a new bufio.ReadWriter for the hijacked connection.
	// 하이재킹된 연결을 위해 새로운 bufio.ReadWriter를 생성합니다.
	hijackedBufWriter := bufio.NewWriter(conn) // Use a new writer for Hijack, don't reuse the pooled one

	return conn, bufio.NewReadWriter(reader, hijackedBufWriter), nil
}

// Hijacked returns whether the connection has been hijacked.
// Hijacked는 연결이 하이재킹되었는지 여부를 반환합니다.
func (w *ResponseWriter) Hijacked() bool {
	return w.hijacked
}

// EndResponse serializes and writes the HTTP response to the connection.
// EndResponse는 HTTP 응답을 직렬화하여 연결에 씁니다.
func (w *ResponseWriter) EndResponse() error {
	if w.hijacked {
		return nil
	}

	// If headers not sent yet, it means no body was written.
	// 헤더가 아직 전송되지 않았다면 바디가 없었다는 의미입니다.
	if !w.headerSent {
		w.header.Set("Content-Length", "0")
		// Fallback to ensuring headers are sent, which will also set Chunked if needed.
		// 이 경우 Content-Length: 0이므로 Chunked가 아님.
		if err := w.ensureHeaderSent(); err != nil {
			return err
		}
	}

	// If chunked, send terminating chunk
	// Chunked 인코딩인 경우 종료 청크 전송
	if w.chunked {
		if _, err := w.bufWriter.Write([]byte("0\r\n\r\n")); err != nil {
			return err
		}
	}

	// Flush any remaining buffered data
	// 버퍼링된 남은 데이터 플러시
	return w.bufWriter.Flush()
}