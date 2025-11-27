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
	"github.com/DevNewbie1826/hon/pkg/bytebufferpool"
)

// ResponseWriter implements http.ResponseWriter and wraps netpoll connection.
// ResponseWriter는 http.ResponseWriter 인터페이스를 구현하며 netpoll 연결을 래핑합니다.
type ResponseWriter struct {
	ctx        *appcontext.RequestContext
	req        *http.Request
	header     http.Header
	statusCode int
	body       *bytebufferpool.ByteBuffer
	hijacked   bool
	headerSent bool
	chunked    bool
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
	w.body = bytebufferpool.Get()
	return w
}

// Release returns the ResponseWriter and its resources to their respective pools.
// Release는 ResponseWriter와 리소스를 각각의 풀로 반환합니다.
func (w *ResponseWriter) Release() {
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

	// Return body buffer to its pool
	// 바디 버퍼를 풀로 반환
	if w.body != nil {
		bytebufferpool.Put(w.body)
		w.body = nil
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
	return w.body.Write(p)
}

// ReadFrom implements io.ReaderFrom.
// ReadFrom은 io.ReaderFrom 인터페이스를 구현합니다.
// It uses copyBufPool to read data and writes directly to the connection to minimize memory usage and copying.
// copyBufPool을 사용하여 데이터를 읽고 연결에 직접 씀으로써 메모리 사용량과 복사를 최소화합니다.
func (w *ResponseWriter) ReadFrom(r io.Reader) (n int64, err error) {
	if w.hijacked {
		return 0, http.ErrHijacked
	}

	// Flush any pending data in w.body first?
	// w.body에 대기 중인 데이터가 있다면 먼저 플러시해야 합니다.
	if w.body.Len() > 0 {
		w.Flush()
	}

	// Ensure headers are sent.
	// 헤더가 전송되었는지 확인합니다.
	if err := w.ensureHeaderSent(); err != nil {
		return 0, err
	}

	conn := w.ctx.Conn()
	bufPtr := copyBufPool.Get().(*[]byte)
	buf := *bufPtr
	defer copyBufPool.Put(bufPtr)

	for {
		nr, er := r.Read(buf)
		if nr > 0 {
			// Write directly to connection
			// 연결에 직접 씁니다.
			if w.chunked {
				// Chunk header
				fmt.Fprintf(conn, "%x\r\n", nr)
				// Chunk data
				if _, ew := conn.Write(buf[:nr]); ew != nil {
					err = ew
					break
				}
				// Chunk trailer
				if _, ew := conn.Write([]byte("\r\n")); ew != nil {
					err = ew
					break
				}
			} else {
				// Normal direct write
				if _, ew := conn.Write(buf[:nr]); ew != nil {
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

	conn := w.ctx.Conn()

	// Check if we need to use chunked encoding
	// Content-Length가 없으면 Chunked 인코딩을 사용합니다.
	if w.header.Get("Content-Length") == "" {
		w.chunked = true
		w.header.Set("Transfer-Encoding", "chunked")
	}

	statusText := http.StatusText(w.statusCode)
	if statusText == "" {
		statusText = "status code " + fmt.Sprintf("%d", w.statusCode)
	}
	headerLine := fmt.Sprintf("HTTP/1.1 %d %s\r\n", w.statusCode, statusText)

	if _, err := conn.Write([]byte(headerLine)); err != nil {
		return err
	}
	if err := w.header.Write(conn); err != nil {
		return err
	}
	if _, err := conn.Write([]byte("\r\n")); err != nil {
		return err
	}
	w.headerSent = true
	return nil
}

// Flush sends any buffered data to the client.
// Flush는 버퍼링된 모든 데이터를 클라이언트로 전송합니다.
func (w *ResponseWriter) Flush() {
	if w.hijacked {
		return
	}

	// 1. Send Headers if not sent yet
	// 1. 아직 헤더를 보내지 않았다면 전송합니다.
	if err := w.ensureHeaderSent(); err != nil {
		return
	}

	// 2. Send buffered body
	// 2. 버퍼링된 바디를 전송합니다.
	if w.body.Len() > 0 {
		conn := w.ctx.Conn()
		if w.chunked {
			// Write chunk header: "Size\r\n"
			fmt.Fprintf(conn, "%x\r\n", w.body.Len())
			// Write chunk data
			conn.Write(w.body.Bytes())
			// Write chunk trailer: "\r\n"
			conn.Write([]byte("\r\n"))
		} else {
			// Normal write
			conn.Write(w.body.Bytes())
		}
		w.body.Reset() // Clear buffer after flushing // 플러시 후 버퍼 초기화
	}
}

// Hijack lets the caller take over the connection.
// Hijack은 호출자가 연결 제어권을 가져가도록 합니다.
func (w *ResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.hijacked {
		return nil, nil, errors.New("already hijacked")
	}
	w.hijacked = true

	conn := w.ctx.Conn()
	reader := w.ctx.GetReader()
	writer := bufio.NewWriter(conn)

	return conn, bufio.NewReadWriter(reader, writer), nil
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

	conn := w.ctx.Conn()
	bodyLen := w.body.Len()

	// If headers already sent (via Flush or ReadFrom)
	// 헤더가 이미 전송된 경우(Flush 또는 ReadFrom을 통해)
	if w.headerSent {
		if bodyLen > 0 {
			if w.chunked {
				fmt.Fprintf(conn, "%x\r\n", bodyLen)
				conn.Write(w.body.Bytes())
				conn.Write([]byte("\r\n"))
			} else {
				conn.Write(w.body.Bytes())
			}
		}
		// If chunked, send terminating chunk
		// Chunked 인코딩인 경우 종료 청크 전송
		if w.chunked {
			conn.Write([]byte("0\r\n\r\n"))
		}
		return nil
	}

	// Normal response (not flushed yet)
	// 일반 응답 (아직 플러시되지 않음)

	// Set Content-Length if not present
	// Content-Length가 없으면 설정합니다.
	if w.header.Get("Content-Length") == "" {
		w.header.Set("Content-Length", fmt.Sprintf("%d", bodyLen))
	}

	// Set Default Content-Type if not present and body is not empty
	// 본문이 비어있지 않고 Content-Type이 없으면 기본값을 설정합니다.
	if bodyLen > 0 && w.header.Get("Content-Type") == "" {
		w.header.Set("Content-Type", http.DetectContentType(w.body.B))
	}

	// Prepare Status Line
	// 상태 라인 준비
	statusText := http.StatusText(w.statusCode)
	if statusText == "" {
		statusText = "status code " + fmt.Sprintf("%d", w.statusCode)
	}
	headerLine := fmt.Sprintf("HTTP/1.1 %d %s\r\n", w.statusCode, statusText)

	// 1. Write Status Line
	// 1. 상태 라인 전송
	if _, err := conn.Write([]byte(headerLine)); err != nil {
		return err
	}

	// 2. Write Headers
	// 2. 헤더 전송
	if err := w.header.Write(conn); err != nil {
		return err
	}

	// 3. Write Header End
	// 3. 헤더 종료 문자 전송
	if _, err := conn.Write([]byte("\r\n")); err != nil {
		return err
	}

	// 4. Write Body
	// 4. 바디 전송
	if bodyLen > 0 {
		if _, err := conn.Write(w.body.Bytes()); err != nil {
			return err
		}
	}

	return nil
}