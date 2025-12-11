package adaptor

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DevNewbie1826/hon/pkg/appcontext"
	"github.com/cloudwego/netpoll"
)

// ReadHandler is a function type for handling custom connection reads (e.g., WebSocket).
type ReadHandler func(conn net.Conn, rw *bufio.ReadWriter) error

// Hijacker is an interface that allows taking over the connection and setting a custom read handler.
// Hijacker는 연결 제어권을 가져와 사용자 정의 읽기 핸들러를 설정할 수 있는 인터페이스입니다.
type Hijacker interface {
	Hijack() (net.Conn, *bufio.ReadWriter, error)
	SetReadHandler(handler ReadHandler)
}

// currentDate holds the cached Date header value.
// currentDate는 캐시된 Date 헤더 값을 저장합니다.
var currentDate atomic.Value
var crlf = []byte("\r\n") // CRLF (Carriage Return Line Feed) bytes. // CRLF (캐리지 리턴 라인 피드) 바이트입니다.
var chunkEnd = []byte("0\r\n\r\n") // The terminating chunk for chunked transfer encoding. // 청크 전송 인코딩의 종료 청크입니다.

func init() {
	// Truncate to the second to ensure consistent update on second boundary
	currentDate.Store(time.Now().UTC().Truncate(time.Second).Format(http.TimeFormat))
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		for range ticker.C {
			currentDate.Store(time.Now().UTC().Truncate(time.Second).Format(http.TimeFormat))
		}
	}()
}

// ResponseWriter implements http.ResponseWriter and wraps netpoll connection.
// ResponseWriter는 http.ResponseWriter 인터페이스를 구현하며 netpoll 연결을 래핑합니다.
type ResponseWriter struct {
	ctx        *appcontext.RequestContext // The request context for this response. // 이 응답에 대한 요청 컨텍스트입니다.
	req        *http.Request              // The HTTP request associated with this response. // 이 응답과 관련된 HTTP 요청입니다.
	header     http.Header                // The response headers. // 응답 헤더입니다.
	statusCode int                        // The HTTP status code to be written. // 작성될 HTTP 상태 코드입니다.
	hijacked   bool                       // Indicates if the connection has been hijacked. // 연결이 하이재킹되었는지 나타냅니다.
	headerSent bool                       // Indicates if headers have already been sent. // 헤더가 이미 전송되었는지 나타냅니다.
	chunked    bool                       // Indicates if chunked transfer encoding is used. // 청크 전송 인코딩이 사용되는지 나타냅니다.
	bufWriter  *bufio.Writer              // Buffering for efficient writes. // 효율적인 쓰기를 위한 버퍼입니다.
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

	// Get bufio.Writer from context (injected by engine)
	// 컨텍스트에서 bufio.Writer를 가져옵니다 (엔진에 의해 주입됨).
	w.bufWriter = w.ctx.GetWriter()

	return w
}

// Release returns the ResponseWriter and its resources to their respective pools.
// Release는 ResponseWriter와 해당 리소스들을 각각의 풀로 반환합니다.
func (w *ResponseWriter) Release() {
	// Note: w.bufWriter is managed by the Engine, so we don't Put it back here.
	// 참고: w.bufWriter는 Engine에 의해 관리되므로, 여기서 반환하지 않습니다.
	w.bufWriter = nil // Avoid lingering pointer

	w.ctx = nil
	w.req = nil
	w.statusCode = 0
	w.hijacked = false
	w.headerSent = false
	w.chunked = false

	// Clear headers
	// 헤더를 초기화합니다.
	clear(w.header)

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
	// 원격 주소를 설정합니다.
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

	// Prevent sending "0\r\n\r\n" which closes the chunked stream prematurely.
	// 청크 스트림이 조기에 종료되는 것을 방지하기 위해 데이터 길이가 0이면 반환합니다.
	if len(p) == 0 {
		return 0, nil
	}

	if !w.headerSent {
		// Sniff Content-Type if not set
		// Content-Type이 설정되지 않았다면 응답 본문의 첫 512바이트를 기반으로 Content-Type을 감지합니다.
		if w.header.Get("Content-Type") == "" { // Sniffing logic simplified as len(p) > 0 check is done above
			// http.DetectContentType only needs the first 512 bytes
			sniffLen := len(p)
			if sniffLen > 512 {
				sniffLen = 512
			}
			w.header.Set("Content-Type", http.DetectContentType(p[:sniffLen]))
		}

		if err := w.ensureHeaderSent(); err != nil {
			return 0, err
		}
	}

	if w.chunked {
		// Chunk header
		var chunkHeaderBuf [20]byte // Small buffer on stack
		hexLen := strconv.AppendInt(chunkHeaderBuf[:0], int64(len(p)), 16)
		if _, err := w.bufWriter.Write(hexLen); err != nil {
			return 0, err
		}
		if _, err := w.bufWriter.Write(crlf); err != nil {
			return 0, err
		}
		// Chunk data
		n, err := w.bufWriter.Write(p)
		if err != nil {
			return n, err // Return n and err even if writing trailer fails
		}
		// Chunk trailer
		if _, err := w.bufWriter.Write(crlf); err != nil {
			return n, err
		}
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
		// 기본 연결이 io.ReaderFrom을 구현하는지 확인합니다.
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
				var chunkHeaderBuf [20]byte // Small buffer on stack
				hexLen := strconv.AppendInt(chunkHeaderBuf[:0], int64(nr), 16)
				if _, ew := w.bufWriter.Write(hexLen); ew != nil {
					err = ew
					break
				}
				if _, ew := w.bufWriter.Write(crlf); ew != nil {
					err = ew
					break
				}
				// Chunk data
				if _, ew := w.bufWriter.Write(buf[:nr]); ew != nil {
					err = ew
					break
				}
				// Chunk trailer
				if _, ew := w.bufWriter.Write(crlf); ew != nil {
					err = ew
					break
				}
			} else {
				// Normal direct write
				// 일반적인 직접 쓰기
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

var (
	headerDate          = []byte("Date: ")           // HTTP Date header key and colon. // HTTP Date 헤더 키와 콜론입니다.
	headerContentLength = "Content-Length"             // HTTP Content-Length header key. // HTTP Content-Length 헤더 키입니다.
	headerContentType   = "Content-Type"               // HTTP Content-Type header key. // HTTP Content-Type 헤더 키입니다.
	headerTransferEnc   = "Transfer-Encoding"          // HTTP Transfer-Encoding header key. // HTTP Transfer-Encoding 헤더 키입니다.
	headerConnection    = "Connection"                 // HTTP Connection header key. // HTTP Connection 헤더 키입니다.
	
	bytesTransferEncodingChunked = []byte("Transfer-Encoding: chunked\r\n") // Pre-computed bytes for chunked transfer encoding header. // 청크 전송 인코딩 헤더를 위한 미리 계산된 바이트입니다.
)
// ensureHeaderSent sends headers if they haven't been sent yet.
// ensureHeaderSent는 헤더가 아직 전송되지 않았다면 전송합니다.
func (w *ResponseWriter) ensureHeaderSent() error {
	if w.headerSent {
		return nil
	}

	statusText := http.StatusText(w.statusCode)
	if statusText == "" {
		statusText = "status code " + strconv.Itoa(w.statusCode)
	}
	headerLine := "HTTP/1.1 " + strconv.Itoa(w.statusCode) + " " + statusText + "\r\n"

	if _, err := w.bufWriter.Write([]byte(headerLine)); err != nil {
		return err
	}

	// Optimization: Write Date header directly to buffer if not present in map.
	// This avoids map allocation/access overhead for every request.
	if w.header.Get("Date") == "" {
		if _, err := w.bufWriter.Write(headerDate); err != nil {
			return err
		}
		if _, err := w.bufWriter.WriteString(currentDate.Load().(string)); err != nil {
			return err
		}
		if _, err := w.bufWriter.Write(crlf); err != nil {
			return err
		}
	}

	// If Content-Length is not set, we must use chunked encoding because we are streaming.
	// Content-Length가 설정되지 않았다면 스트리밍 중이므로 Chunked 인코딩을 사용해야 합니다.
	if w.header.Get(headerContentLength) == "" {
		w.chunked = true
		// Optimization: Don't set in map to avoid allocation/hashing. We write it directly below.
		// w.header.Set(headerTransferEnc, "chunked")
	} else {
		w.chunked = false
	}

	// Set Default Content-Type if not present
	// Content-Type이 없으면 기본값(text/plain 또는 application/octet-stream)을 추론하기 어렵습니다.
	// 표준 라이브러리는 Sniffing을 하지만 여기서는 생략하거나 기본값만 처리합니다.
	// 현재는 여기에서 Content-Type을 자동으로 설정하지 않으며, 사용자가 명시적으로 설정해야 합니다.

	if err := w.header.Write(w.bufWriter); err != nil {
		return err
	}

	// Fast Path: Write Transfer-Encoding directly
	if w.chunked && w.header.Get(headerTransferEnc) == "" {
		if _, err := w.bufWriter.Write(bytesTransferEncodingChunked); err != nil {
			return err
		}
	}

	if _, err := w.bufWriter.Write(crlf); err != nil {
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
	// Reuse the existing bufWriter which is managed by the Engine.
	// Since the connection will be closed eventually, the Engine will reclaim it.
	// Engine이 관리하는 기존 bufWriter를 재사용합니다.
	// 연결이 결국 닫히게 되므로 Engine이 이를 회수할 것입니다.
	writer := w.bufWriter
	w.bufWriter = nil // Detach from ResponseWriter to prevent accidental use

	// Wrap the connection with BufferedConn to ensure libraries reading from
	// net.Conn (skipping bufio.Reader) still get the buffered data.
	// BufferedConn으로 연결을 래핑하여 (bufio.Reader를 건너뛰고) net.Conn에서 읽는 라이브러리도
	// 버퍼링된 데이터를 계속 받을 수 있도록 합니다.
	wrappedConn := &BufferedConn{
		Connection: conn,
		Reader:     reader,
	}

	// Create a NEW bufio.Reader wrapping the BufferedConn.
	// This breaks the potential recursion cycle if the consumer (e.g., coder/websocket)
	// calls Reset(conn) on the returned bufio.Reader.
	// Cycle avoided: newReader -> BufferedConn -> originalReader -> netpoll.Conn
	// BufferedConn을 감싸는 새로운 bufio.Reader를 생성합니다.
	// 이는 소비자(예: coder/websocket)가 반환된 bufio.Reader에서 Reset(conn)을 호출할 경우
	// 발생할 수 있는 재귀 사이클을 방지합니다.
	// 사이클 방지: newReader -> BufferedConn -> originalReader -> netpoll.Conn
	newReader := bufio.NewReader(wrappedConn)

	return wrappedConn, bufio.NewReadWriter(newReader, writer), nil
}

// BufferedConn wraps netpoll.Connection and a bufio.Reader.
// It ensures that reads go through the bufio.Reader to avoid data loss
// if the library using the connection bypasses the bufio.ReadWriter.
// BufferedConn은 netpoll.Connection과 bufio.Reader를 래핑합니다.
// 연결을 사용하는 라이브러리가 bufio.ReadWriter를 우회하여 읽을 경우
// 데이터 손실을 방지하기 위해 읽기 작업이 bufio.Reader를 통해 이루어지도록 합니다.
type BufferedConn struct {
	netpoll.Connection
	Reader *bufio.Reader
}

// Read reads from the embedded bufio.Reader.
// Read는 임베디드된 bufio.Reader로부터 데이터를 읽습니다.
func (c *BufferedConn) Read(p []byte) (n int, err error) {
	// 1. First, drain any buffered data from the bufio.Reader
	// 1. 먼저 bufio.Reader의 버퍼링된 데이터를 모두 소진합니다.
	if c.Reader.Buffered() > 0 {
		return c.Reader.Read(p)
	}

	// 2. Read directly from the underlying connection.
	// Since the engine keeps the ServeConn handler active (blocking mode),
	// netpoll will correctly feed data to this Read call.
	// 2. 기본 연결에서 직접 읽습니다.
	// 엔진이 ServeConn 핸들러를 활성 상태(블로킹 모드)로 유지하므로,
	// netpoll은 이 Read 호출에 데이터를 올바르게 공급할 것입니다.
	return c.Connection.Read(p)
}


// Hijacked returns whether the connection has been hijacked.
// Hijacked는 연결이 하이재킹되었는지 여부를 반환합니다.
func (w *ResponseWriter) Hijacked() bool {
	return w.hijacked
}

// SetReadHandler sets the custom read handler for the connection.
// SetReadHandler는 연결에 대한 사용자 정의 읽기 핸들러를 설정합니다.
func (w *ResponseWriter) SetReadHandler(h ReadHandler) {
	if w.ctx == nil {
		return
	}
	w.ctx.SetReadHandler(func(c netpoll.Connection, rw *bufio.ReadWriter) error {
		return h(c, rw)
	})
}

// HeaderSent returns whether the headers have already been sent.
// HeaderSent는 헤더가 이미 전송되었는지 여부를 반환합니다.
func (w *ResponseWriter) HeaderSent() bool {
	return w.headerSent
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
	// Chunked 인코딩인 경우 종료 청크를 전송합니다.
	if w.chunked {
		if _, err := w.bufWriter.Write(chunkEnd); err != nil {
			return err
		}
	}

	// Flush any remaining buffered data
	// 버퍼링된 남은 데이터를 플러시합니다.
	return w.bufWriter.Flush()
}

// -----------------------------------------------------------------------------
// Optional Interfaces Implementation for http.ResponseController & Compatibility
// -----------------------------------------------------------------------------

// Unwrap returns the underlying ResponseWriter.
// Since this is the root writer, it returns nil.
// Unwrap은 기반 ResponseWriter를 반환합니다. 이 객체가 루트이므로 nil을 반환합니다.
func (w *ResponseWriter) Unwrap() http.ResponseWriter {
	return nil
}

// SetReadDeadline sets the read deadline on the underlying connection.
// Supported by http.ResponseController (Go 1.20+).
// SetReadDeadline은 기본 연결에 대한 읽기 마감 시간을 설정합니다.
// http.ResponseController(Go 1.20+)에서 지원됩니다.
func (w *ResponseWriter) SetReadDeadline(deadline time.Time) error {
	if w.ctx == nil || w.ctx.Conn() == nil {
		return errors.New("connection not available")
	}
	return w.ctx.Conn().SetReadDeadline(deadline)
}

// SetWriteDeadline sets the write deadline on the underlying connection.
// Supported by http.ResponseController (Go 1.20+).
// SetWriteDeadline은 기본 연결에 대한 쓰기 마감 시간을 설정합니다.
// http.ResponseController(Go 1.20+)에서 지원됩니다.
func (w *ResponseWriter) SetWriteDeadline(deadline time.Time) error {
	if w.ctx == nil || w.ctx.Conn() == nil {
		return errors.New("connection not available")
	}
	return w.ctx.Conn().SetWriteDeadline(deadline)
}

// EnableFullDuplex indicates that the request handler will read from the request body
// concurrently with writing the response body.
// Supported by http.ResponseController (Go 1.21+).
// EnableFullDuplex는 요청 핸들러가 응답 본문을 쓰는 것과 동시에 요청 본문에서 읽을 것임을 나타냅니다.
// http.ResponseController(Go 1.21+)에서 지원됩니다.
func (w *ResponseWriter) EnableFullDuplex() error {
	// Hon engine supports full duplex by design (ServeConn loop vs Request Handler).
	// However, usually hijacking is preferred for true full duplex protocols like WebSocket.
	// Hon 엔진은 설계상 전이중 통신을 지원합니다 (ServeConn 루프 vs 요청 핸들러).
	// 하지만 WebSocket과 같은 진정한 전이중 프로토콜에는 일반적으로 하이재킹이 선호됩니다.
	return nil
}

// CloseNotify implements http.CloseNotifier.
// It returns a channel that receives a value when the client connection has gone away.
// Deprecated: Use context.Context from http.Request instead.
// CloseNotify는 http.CloseNotifier를 구현합니다.
// 클라이언트 연결이 끊어지면 값을 수신하는 채널을 반환합니다.
// Deprecated: 대신 http.Request의 context.Context를 사용하세요.
func (w *ResponseWriter) CloseNotify() <-chan bool {
	ch := make(chan bool, 1)
	if w.ctx == nil {
		ch <- true
		return ch
	}
	
	// Use the request context's Done channel
	// 요청 컨텍스트의 Done 채널을 사용합니다.
	go func() {
		<-w.ctx.Req().Done()
		ch <- true
	}()
	return ch
}

// WriteString implements io.StringWriter.
// It optimizes writing strings without converting to byte slice.
// WriteString은 io.StringWriter를 구현합니다.
// 바이트 슬라이스로 변환하지 않고 문자열 쓰기를 최적화합니다.
func (w *ResponseWriter) WriteString(s string) (int, error) {
	if w.hijacked {
		return 0, http.ErrHijacked
	}

	// Prevent sending "0\r\n\r\n" which closes the chunked stream prematurely.
	if len(s) == 0 {
		return 0, nil
	}

	if !w.headerSent {
		if err := w.ensureHeaderSent(); err != nil {
			return 0, err
		}
	}
	// bufio.Writer has WriteString, so we can use it directly if available.
	// But our w.bufWriter is *bufio.Writer, so yes.
	// bufio.Writer는 WriteString을 가지고 있으므로, 사용 가능하다면 직접 사용할 수 있습니다.
	// w.bufWriter는 *bufio.Writer이므로 가능합니다.

	if w.chunked {
		// Optimization: Avoid conversion to []byte by writing chunk parts directly.
		// 최적화: 청크 파트를 직접 작성하여 []byte로의 변환을 피합니다.
		
		// Chunk header
		var chunkHeaderBuf [20]byte
		hexLen := strconv.AppendInt(chunkHeaderBuf[:0], int64(len(s)), 16)
		if _, err := w.bufWriter.Write(hexLen); err != nil {
			return 0, err
		}
		if _, err := w.bufWriter.Write(crlf); err != nil {
			return 0, err
		}
		// Chunk data
		n, err := w.bufWriter.WriteString(s)
		if err != nil {
			return n, err
		}
		// Chunk trailer
		if _, err := w.bufWriter.Write(crlf); err != nil {
			return n, err
		}
		return n, err
	}

	return w.bufWriter.WriteString(s)
}
