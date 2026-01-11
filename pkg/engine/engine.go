package engine

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"runtime/debug"
	"sync"
	"sync/atomic" // atomic 패키지 임포트 추가
	"time"

	"github.com/DevNewbie1826/hon/pkg/adaptor"
	"github.com/DevNewbie1826/hon/pkg/appcontext"
	"github.com/DevNewbie1826/hon/pkg/engine/parser"
	"github.com/cloudwego/netpoll"
)

// MaxDrainSize is the maximum number of bytes to drain from a request body
// before deciding to close the connection instead of keeping it alive.
// This prevents malicious clients from sending very large bodies and
// tying up server resources if the handler doesn't consume the body.
// MaxDrainSize는 요청 본문에서 드레인할 수 있는 최대 바이트 수입니다.
// 핸들러가 본문을 소비하지 않을 경우, 악의적인 클라이언트가 매우 큰 본문을 보내
// 서버 리소스를 고갈시키는 것을 방지하기 위함입니다.
const MaxDrainSize = 64 * 1024 // 64KB

// connectionStatePool recycles ConnectionState objects to reduce GC pressure.
// connectionStatePool은 가비지 컬렉션(GC) 부하를 줄이기 위해 ConnectionState 객체를 재활용합니다.
var connectionStatePool = sync.Pool{
	New: func() any {
		return new(ConnectionState)
	},
}

// ConnectionState holds the state of a connection, including buffers and custom handler.
// ConnectionState는 연결의 상태를 나타내며, 버퍼 및 사용자 정의 핸들러를 포함합니다.
type ConnectionState struct {
	Reader      *bufio.Reader          // Reusable bufio.Reader for the connection.
	Writer      *bufio.Writer          // Reusable bufio.Writer for the connection.
	ReadHandler appcontext.ReadHandler // Custom read handler for protocols like WebSocket.
	CancelFunc  context.CancelFunc     // Function to cancel the request context.
	ReadTimeout time.Duration          // The configured read timeout for the connection.
	Processing  atomic.Bool            // Atomic flag to prevent concurrent processing of the same connection.
}

// NewConnectionState retrieves a ConnectionState from the pool and initializes it.
// NewConnectionState는 풀에서 ConnectionState를 가져와 초기화합니다.
func NewConnectionState(readTimeout time.Duration, cancel context.CancelFunc) *ConnectionState {
	s := connectionStatePool.Get().(*ConnectionState)
	s.ReadTimeout = readTimeout
	s.CancelFunc = cancel
	return s
}

// Reset resets the state object. (Buffers are handled by Engine)
func (s *ConnectionState) Reset() {
	s.Reader = nil
	s.Writer = nil
	s.CancelFunc = nil
	s.ReadHandler = nil
	s.Processing.Store(false)
	s.ReadTimeout = 0
}

// CtxKeyConnectionState is the context key for retrieving ConnectionState.
// CtxKeyConnectionState는 ConnectionState를 검색하기 위한 컨텍스트 키입니다.
var CtxKeyConnectionState = struct{}{}

// Option is a function type for configuring the Engine.
// Option은 Engine 설정을 위한 함수 타입입니다.
type Option func(*Engine)

// WithRequestTimeout sets the request processing timeout.
// WithRequestTimeout은 요청 처리 타임아웃을 설정합니다.
func WithRequestTimeout(d time.Duration) Option {
	return func(e *Engine) {
		e.requestTimeout = d
	}
}

// WithMaxDrainSize sets the maximum body size to drain.
// WithMaxDrainSize는 드레인할 최대 본문 크기를 설정합니다.
func WithMaxDrainSize(size int64) Option {
	return func(e *Engine) {
		e.maxDrainSize = size
	}
}

// WithBufferSize sets the size of the bufio buffers.
func WithBufferSize(size int) Option {
	return func(e *Engine) {
		e.bufferSize = size
	}
}

// Engine is the core structure for processing HTTP requests.
// Engine은 HTTP 요청을 처리하는 핵심 구조체입니다.
type Engine struct {
	Handler        http.Handler  // The HTTP handler to process requests.
	requestTimeout time.Duration // Timeout for processing individual requests.
	maxDrainSize   int64         // Max bytes to drain from body.
	bufferSize     int           // Size of the bufio buffers.

	readerPool sync.Pool // Pool for bufio.Reader
	writerPool sync.Pool // Pool for bufio.Writer
}

// NewEngine creates a new Engine.
// NewEngine은 새로운 Engine을 생성합니다.
func NewEngine(handler http.Handler, opts ...Option) *Engine {
	e := &Engine{
		Handler:      handler,
		maxDrainSize: MaxDrainSize, // Default 64KB
		bufferSize:   4096,         // Default 4KB
	}
	for _, opt := range opts {
		opt(e)
	}

	e.readerPool = sync.Pool{
		New: func() any {
			return bufio.NewReaderSize(nil, e.bufferSize)
		},
	}
	e.writerPool = sync.Pool{
		New: func() any {
			return bufio.NewWriterSize(nil, e.bufferSize)
		},
	}

	return e
}

// ReleaseConnectionState returns buffers to the engine's pools and the state to the global pool.
// ReleaseConnectionState는 버퍼를 엔진 풀로 반환하고 상태 객체를 글로벌 풀로 반환합니다.
func (e *Engine) ReleaseConnectionState(s *ConnectionState) {
	if s.Reader != nil {
		e.readerPool.Put(s.Reader)
	}
	if s.Writer != nil {
		e.writerPool.Put(s.Writer)
	}
	s.Reset()
	connectionStatePool.Put(s)
}

// ServeConn is used as netpoll's OnRequest callback.
// ServeConn은 netpoll의 OnRequest 콜백으로 사용됩니다.
func (e *Engine) ServeConn(ctx context.Context, conn netpoll.Connection) error {
	stateVal := ctx.Value(CtxKeyConnectionState)
	if stateVal == nil {
		return errors.New("connection state not found")
	}
	state := stateVal.(*ConnectionState)

	// Try to acquire processing lock
	// 처리 잠금을 획득하려고 시도합니다.
	if !state.Processing.CompareAndSwap(false, true) {
		// If already processing, return nil to tell netpoll to try again later
		// 이미 처리 중이라면, 나중에 다시 시도하도록 netpoll에 nil을 반환합니다.
		return nil
	}

	// Initialize buffers if they are not already set (reused from state)
	// 버퍼가 아직 설정되지 않았다면 초기화합니다 (상태에서 재사용됨).
	if state.Reader == nil {
		state.Reader = e.readerPool.Get().(*bufio.Reader)
		state.Reader.Reset(conn)
	}
	if state.Writer == nil {
		state.Writer = e.writerPool.Get().(*bufio.Writer)
		state.Writer.Reset(conn)
	}

	// Dispatch to Custom ReadHandler (e.g., WebSocket) if set
	// 사용자 정의 ReadHandler(예: WebSocket)가 설정되어 있다면 디스패치합니다.
	if state.ReadHandler != nil {
		// Run ReadHandler synchronously in the netpoll worker goroutine
		// netpoll 워커 고루틴에서 ReadHandler를 동기적으로 실행합니다.
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[Panic] Recovered in ReadHandler: %v\n%s", r, debug.Stack())
					conn.Close()
				}
				state.Processing.Store(false)
			}()
			rw := bufio.NewReadWriter(state.Reader, state.Writer)
			// We ignore the error here as the handler should handle it or close the connection
			// 핸들러가 오류를 처리하거나 연결을 닫아야 하므로, 여기서 오류는 무시합니다.
			if err := state.ReadHandler(conn, rw); err != nil {
				log.Printf("ReadHandler error: %v", err)
				conn.Close() // Ensure connection is closed on error
			}
		}()
		return nil
	}

	// Dispatch to HTTP Handler synchronously in the netpoll worker goroutine
	// netpoll 워커 고루틴에서 HTTP 핸들러로 동기적으로 디스패치합니다.
	e.serveHTTP(ctx, conn, state)

	return nil
}

// serveHTTP handles HTTP requests in a goroutine.
// serveHTTP는 고루틴에서 HTTP 요청을 처리합니다.
func (e *Engine) serveHTTP(ctx context.Context, conn netpoll.Connection, state *ConnectionState) {
	// Loop to handle pipelined requests or buffered data
	// 파이프라인된 요청 또는 버퍼링된 데이터를 처리하기 위한 루프입니다.
	for {
		// Check if connection is closed before processing
		// 처리 전에 연결이 닫혔는지 확인합니다.
		if !conn.IsActive() {
			state.Processing.Store(false)
			return
		}

		// Optimization: Lightweight Parser Check
		// If bufio buffer is empty, we check the underlying netpoll buffer to see if we have a full request.
		// If not, we yield the goroutine to avoid blocking inside http.ReadRequest.
		if state.Reader.Buffered() == 0 {
			r := conn.Reader()
			if r != nil {
				if r.Len() == 0 {
					// No data in netpoll buffer.
					// We yield.
					state.Processing.Store(false)
					return
				}

				// Peek all available data in Netpoll buffer without advancing
				peekBuf, _ := r.Peek(r.Len())
				res := parser.CheckRequest(peekBuf)

				if !res.Complete {
					// Request is incomplete. Return to yield the worker.
					// Netpoll will call ServeConn again when more data arrives.
					state.Processing.Store(false)
					return
				}
			}
		}

		requestContext := appcontext.NewRequestContext(conn, ctx, state.Reader, state.Writer)
		requestContext.SetOnSetReadHandler(func(h appcontext.ReadHandler) {
			state.ReadHandler = h
		})

		req, hijacked, err := e.handleRequest(requestContext)
		requestContext.Release() // Release context after use // 사용 후 컨텍스트를 해제합니다.

		if err != nil {
			// Handle specific errors that might indicate no more data or closed connection
			// 더 이상 데이터가 없거나 연결이 닫혔음을 나타낼 수 있는 특정 오류를 처리합니다.
			if err != io.EOF { // io.EOF typically means client closed connection gracefully. // io.EOF는 일반적으로 클라이언트가 연결을 정상적으로 닫았음을 의미합니다.
				conn.Close()
			}
			state.Processing.Store(false)
			return
		}

		if hijacked {
			// If hijacked, the connection is now under the control of the hijacker (or the ReadHandler if set).
			// We stop standard HTTP processing for this connection.
			// 연결이 하이재킹되면, 이제 연결은 하이재커(또는 설정된 ReadHandler)의 제어 하에 있습니다.
			// 이 연결에 대한 표준 HTTP 처리를 중지합니다.

			// Clear Deadlines for long-lived connections (WebSocket, etc.)
			// 장기 연결(예: WebSocket)을 위해 데드라인을 지웁니다.
			if err := conn.SetReadDeadline(time.Time{}); err != nil {
				log.Printf("Failed to clear read deadline: %v", err)
			}
			if err := conn.SetWriteDeadline(time.Time{}); err != nil {
				log.Printf("Failed to clear write deadline: %v", err)
			}

			if state.ReadHandler != nil {
				// Event-Driven Hijack (e.g., gobwas/ws with ReadHandler)
				// This mode is efficient as it frees the netpoll worker.
				// 이벤트 기반 하이재킹 (예: ReadHandler를 사용하는 gobwas/ws).
				// 이 모드는 netpoll 워커를 해방하므로 효율적입니다.
				// We MUST return from serveHTTP (and ServeConn) to allow netpoll to trigger
				// ServeConn again when new data arrives.
				// 새로운 데이터가 도착할 때 netpoll이 ServeConn을 다시 트리거하도록
				// serveHTTP (및 ServeConn)에서 반환해야 합니다.
				// We release the lock so the next ServeConn call can proceed.
				// 다음 ServeConn 호출이 진행될 수 있도록 잠금을 해제합니다.
				state.Processing.Store(false)
				return
			}

			// Standard Hijack (e.g., gorilla/websocket with loop in separate goroutine)
			// This mode blocks a netpoll worker goroutine. Use SetReadHandler for better scalability.
			// 표준 하이재킹 (예: 별도 고루틴에서 루프를 사용하는 gorilla/websocket).
			// 이 모드는 netpoll 워커 고루틴을 블로킹합니다. 더 나은 확장성을 위해 SetReadHandler를 사용하세요.
			// Important: We do NOT release the processing lock (state.Processing remains true).
			// This prevents ServeConn from spawning new goroutines for this connection,
			// effectively handing over exclusive control to the hijacker.
			// 중요: 처리 잠금(state.Processing)을 해제하지 않습니다 (true 상태 유지).
			// 이는 ServeConn이 이 연결에 대해 새로운 고루틴을 생성하는 것을 방지하고,
			// 하이재커에게 독점적인 제어권을 효과적으로 넘겨줍니다.

			// Wait until the connection is closed.
			// Since we are running synchronously in ServeConn (netpoll worker),
			// keeping this function alive ensures netpoll considers the connection active/busy.
			// The hijacker (e.g., websocket library) will handle I/O in its own goroutine or loop.
			// 연결이 닫힐 때까지 기다립니다.
			// ServeConn (netpoll 워커)에서 동기적으로 실행되므로,
			// 이 함수를 활성 상태로 유지하면 netpoll이 연결을 활성/사용 중으로 간주합니다.
			// 하이재커(예: 웹소켓 라이브러리)는 자체 고루틴 또는 루프에서 I/O를 처리할 것입니다.
			<-ctx.Done() // Block here to keep the worker attached to this connection. // 이 연결에 워커를 연결 상태로 유지하기 위해 여기서 블록합니다.

			state.Processing.Store(false)
			return
		}

		// Body Draining: Read and discard remaining body data for the next request.
		// 바디 드레이닝: 다음 요청을 위해 남은 본문 데이터를 읽고 버립니다.
		if req.Body != nil {
			// Set a short deadline for draining to prevent blocking on slow clients
			// 느린 클라이언트에서 블로킹되는 것을 방지하기 위해 드레이닝에 짧은 데드라인을 설정합니다.
			if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
				log.Printf("Failed to set drain deadline: %v", err)
			}

			// Limit the amount of body we drain to prevent resource exhaustion.
			// 리소스 고갈을 방지하기 위해 드레인할 본문의 양을 제한합니다.
			n, _ := io.Copy(io.Discard, io.LimitReader(req.Body, e.maxDrainSize+1))
			_ = req.Body.Close()

			// Reset deadline
			// 데드라인을 재설정합니다.
			if err := conn.SetReadDeadline(time.Time{}); err != nil {
				log.Printf("Failed to reset read deadline: %v", err)
			}

			if n > e.maxDrainSize {
				req.Close = true
			}
		}

		// Keep-alive logic
		// Keep-alive 로직입니다.
		if req.Close || req.Header.Get("Connection") == "close" {
			conn.Close()
			state.Processing.Store(false)
			return
		}

		// Restore ReadTimeout for the next request (Keep-Alive)
		// 다음 요청(Keep-Alive)을 위해 ReadTimeout을 복원합니다.
		if err := conn.SetReadDeadline(time.Time{}); err != nil {
			log.Printf("Failed to reset read deadline (KA): %v", err)
		}
		if state.ReadTimeout > 0 {
			if err := conn.SetReadTimeout(state.ReadTimeout); err != nil {
				log.Printf("Failed to restore read timeout: %v", err)
			}
		}

		// Check if there is more data in the buffer to process immediately
		// 즉시 처리할 버퍼에 더 많은 데이터가 있는지 확인합니다.
		if state.Reader.Buffered() > 0 {
			continue
		}

		// Double-Check Locking logic to prevent event loss
		// 이벤트 손실을 방지하기 위한 이중 확인 잠금 로직입니다.
		// 1. Release the lock tentatively
		// 1. 잠금을 잠정적으로 해제합니다.
		state.Processing.Store(false)

		// 2. Check buffer again immediately
		// 즉시 버퍼를 다시 확인합니다.
		hasData := state.Reader.Buffered() > 0
		if !hasData {
			if r := conn.Reader(); r != nil && r.Len() > 0 {
				hasData = true
			}
		}

		if hasData {
			// New data arrived right after we checked! Try to re-acquire lock.
			// 확인 직후 새 데이터가 도착했습니다! 잠금을 다시 획득하려고 시도합니다.
			if state.Processing.CompareAndSwap(false, true) {
				// Successfully re-acquired, continue processing
				// 성공적으로 다시 획득하여 처리를 계속합니다.
				continue
			}
			// Failed to acquire: another goroutine (ServeConn) picked it up. Safe to exit.
			// 획득 실패: 다른 고루틴(ServeConn)이 이를 처리했습니다. 안전하게 종료합니다.
			return
		}

		// If no buffered data, we exit the loop.
		// Netpoll will trigger ServeConn again when new data arrives at the socket.
		// 버퍼링된 데이터가 없으면 루프를 종료합니다.
		// Netpoll은 소켓에 새 데이터가 도착하면 ServeConn을 다시 트리거합니다.
		return
	}
}

// handleRequest processes a single HTTP request and returns the processed request object and hijacking status.
// handleRequest는 단일 HTTP 요청을 처리하고, 처리된 요청 객체와 하이재킹 여부를 반환합니다.
func (e *Engine) handleRequest(ctx *appcontext.RequestContext) (*http.Request, bool, error) {
	req, err := adaptor.GetRequest(ctx)
	if err != nil {
		return nil, false, err
	}

	respWriter := adaptor.NewResponseWriter(ctx, req)
	defer respWriter.Release()

	// Apply Request Timeout.
	// 요청 타임아웃 적용.
	if e.requestTimeout > 0 {
		timeoutCtx, cancel := context.WithTimeout(req.Context(), e.requestTimeout)
		req = req.WithContext(timeoutCtx)
		defer cancel()
	}

	// Panic Recovery
	// 패닉 복구
	var panicked bool
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
				log.Printf("[Panic] Recovered in handler: %v\n%s", r, debug.Stack())
				if respWriter.HeaderSent() {
					// If headers were already sent, we cannot send a 500.
					// Log the situation and let the connection be closed by returning an error from handleRequest.
					log.Printf("[Panic] Headers already sent, cannot write 500 status. Forcing connection close.")
				} else {
					// If headers were not sent, we can still write a 500.
					respWriter.WriteHeader(http.StatusInternalServerError)
					// Ensure the response is ended if a panic occurs before EndResponse is called.
					// This will ensure 500 header is sent and potentially an empty body.
					_ = respWriter.EndResponse()
				}
			}
		}()
		e.Handler.ServeHTTP(respWriter, req)
	}()

	if panicked {
		return nil, false, errors.New("handler panicked")
	}

	err = respWriter.EndResponse()
	if err != nil {
		return nil, false, err
	}

	return req, respWriter.Hijacked(), nil
}
