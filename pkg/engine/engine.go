package engine

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log"
	"runtime/debug"
	"sync"
	"sync/atomic" // atomic 패키지 임포트 추가
	"net/http"
	"time"

	"github.com/DevNewbie1826/hon/pkg/adaptor"
	"github.com/DevNewbie1826/hon/pkg/appcontext"
	"github.com/cloudwego/netpoll"
)

// MaxDrainSize is the maximum number of bytes to drain from a request body
// before deciding to close the connection instead of keeping it alive.
// This prevents malicious clients from sending very large bodies and
// tying up server resources if the handler doesn't consume the body.
const MaxDrainSize = 64 * 1024 // 64KB

// bufReaderPool recycles bufio.Reader objects.
var bufReaderPool = sync.Pool{
	New: func() any {
		return bufio.NewReader(nil)
	},
}

// bufWriterPool recycles bufio.Writer objects.
var bufWriterPool = sync.Pool{
	New: func() any {
		return bufio.NewWriter(nil)
	},
}

// ConnectionState holds the state of a connection, including buffers and custom handler.
type ConnectionState struct {
	Reader      *bufio.Reader
	Writer      *bufio.Writer
	ReadHandler appcontext.ReadHandler
	Processing  atomic.Bool // Atomic flag to prevent concurrent processing
	ReadTimeout time.Duration
	CancelFunc  context.CancelFunc
}

// Release returns the reader and writer to their respective pools.
func (s *ConnectionState) Release() {
	if s.Reader != nil {
		bufReaderPool.Put(s.Reader)
		s.Reader = nil
	}
	if s.Writer != nil {
		bufWriterPool.Put(s.Writer)
		s.Writer = nil
	}
}

// CtxKeyConnectionState is the context key for retrieving ConnectionState.
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

// Engine is the core structure for processing HTTP requests.
// Engine은 HTTP 요청을 처리하는 핵심 구조체입니다.
type Engine struct {
	Handler        http.Handler
	requestTimeout time.Duration
}

// NewEngine creates a new Engine.
// NewEngine은 새로운 Engine을 생성합니다.
func NewEngine(handler http.Handler, opts ...Option) *Engine {
	e := &Engine{
		Handler: handler,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
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
	if !state.Processing.CompareAndSwap(false, true) {
		return nil
	}

	// Initialize buffers if they are not already set (reused from state)
	if state.Reader == nil {
		state.Reader = bufReaderPool.Get().(*bufio.Reader)
		state.Reader.Reset(conn)
	}
	if state.Writer == nil {
		state.Writer = bufWriterPool.Get().(*bufio.Writer)
		state.Writer.Reset(conn)
	}

	// Dispatch to Custom ReadHandler (e.g., WebSocket) if set
	if state.ReadHandler != nil {
		// Run ReadHandler synchronously in the netpoll worker goroutine
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
			_ = state.ReadHandler(conn, rw)
		}()
		return nil
	}

	// Dispatch to HTTP Handler synchronously in the netpoll worker goroutine
	e.serveHTTP(ctx, conn, state)

	return nil
}

// serveHTTP handles HTTP requests in a goroutine.
func (e *Engine) serveHTTP(ctx context.Context, conn netpoll.Connection, state *ConnectionState) {
	// Loop to handle pipelined requests or buffered data
	for {
		// Check if connection is closed before processing
		if !conn.IsActive() {
			state.Processing.Store(false)
			return
		}

		requestContext := appcontext.NewRequestContext(conn, ctx, state.Reader, state.Writer)
		requestContext.SetOnSetReadHandler(func(h appcontext.ReadHandler) {
			state.ReadHandler = h
		})

		req, hijacked, err := e.handleRequest(requestContext)
		requestContext.Release() // Release context after use

		if err != nil {
			// Handle specific errors that might indicate no more data or closed connection
			if err != io.EOF {
				conn.Close()
			}
			state.Processing.Store(false)
			return
		}

		if hijacked {
			// If hijacked, the connection is now under the control of the hijacker (or the ReadHandler if set).
			// We stop standard HTTP processing for this connection.
			
			// Clear Deadlines for long-lived connections (WebSocket, etc.)
			_ = conn.SetReadDeadline(time.Time{})
			_ = conn.SetWriteDeadline(time.Time{})

			if state.ReadHandler != nil {
				// Event-Driven Hijack (e.g., gobwas/ws with ReadHandler)
				// We MUST return from serveHTTP (and ServeConn) to allow netpoll to trigger
				// ServeConn again when new data arrives.
				// We release the lock so the next ServeConn call can proceed.
				state.Processing.Store(false)
				return
			}

			// Standard Hijack (e.g., gorilla/websocket with loop in separate goroutine)
			// Important: We do NOT release the processing lock (state.Processing remains true).
			// This prevents ServeConn from spawning new goroutines for this connection,
			// effectively handing over exclusive control to the hijacker.
			
            // Wait until the connection is closed.
            // Since we are running synchronously in ServeConn (netpoll worker),
            // keeping this function alive ensures netpoll considers the connection active/busy.
            // The hijacker (e.g., websocket library) will handle I/O in its own goroutine or loop.
            <-ctx.Done() // Block here to keep the worker attached to this connection.
			
			state.Processing.Store(false)
			return
		}

		// Body Draining: Read and discard remaining body data for the next request.
		if req.Body != nil {
			// Set a short deadline for draining to prevent blocking on slow clients
			_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))

			// Limit the amount of body we drain to prevent resource exhaustion.
			n, _ := io.Copy(io.Discard, io.LimitReader(req.Body, MaxDrainSize+1))
			_ = req.Body.Close()

			// Reset deadline
			_ = conn.SetReadDeadline(time.Time{})

			if n > MaxDrainSize {
				req.Close = true
			}
		}

		// Keep-alive logic
		if req.Close || req.Header.Get("Connection") == "close" {
			conn.Close()
			state.Processing.Store(false)
			return
		}

		// Restore ReadTimeout for the next request (Keep-Alive)
		_ = conn.SetReadDeadline(time.Time{})
		if state.ReadTimeout > 0 {
			_ = conn.SetReadTimeout(state.ReadTimeout)
		}

		// Check if there is more data in the buffer to process immediately
		if state.Reader.Buffered() > 0 {
			continue
		}

		// Double-Check Locking logic to prevent event loss
		// 1. Release the lock tentatively
		state.Processing.Store(false)

		// 2. Check buffer again immediately
		hasData := state.Reader.Buffered() > 0
		if !hasData {
			if r := conn.Reader(); r != nil && r.Len() > 0 {
				hasData = true
			}
		}

		if hasData {
			// New data arrived right after we checked! Try to re-acquire lock.
			if state.Processing.CompareAndSwap(false, true) {
				// Successfully re-acquired, continue processing
				continue
			}
			// Failed to acquire: another goroutine (ServeConn) picked it up. Safe to exit.
			return
		}

		// If no buffered data, we exit the loop.
		// Netpoll will trigger ServeConn again when new data arrives at the socket.
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