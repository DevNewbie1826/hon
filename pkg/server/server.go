package server

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"github.com/DevNewbie1826/hon/pkg/engine"
	"github.com/cloudwego/netpoll"
	"github.com/valyala/fasthttp/reuseport"
)

// Server is the top-level structure for the netpoll server.
// Server는 netpoll 서버의 최상위 구조체입니다.
type Server struct {
	Engine           *engine.Engine    // The request processing engine. // 요청 처리 엔진입니다.
	eventLoop        netpoll.EventLoop // The netpoll event loop. // netpoll 이벤트 루프입니다.
	keepAliveTimeout time.Duration     // Timeout for idle connections. // 유휴 연결에 대한 타임아웃입니다.
	readTimeout      time.Duration     // Timeout for reading request data. // 요청 데이터 읽기에 대한 타임아웃입니다.
	writeTimeout     time.Duration     // Timeout for writing response data. // 응답 데이터 쓰기에 대한 타임아웃입니다.
	maxConns         int32             // Maximum concurrent connections.
	connsCount       int32             // Current connection count.
}

// Option is a function type for configuring the Server.
// Option은 서버 설정을 위한 함수 타입입니다.
type Option func(*Server)

// WithMaxConns sets the maximum number of concurrent connections.
func WithMaxConns(n int32) Option {
	return func(s *Server) {
		s.maxConns = n
	}
}

// WithReadTimeout sets the read timeout.
// WithReadTimeout은 읽기 타임아웃을 설정합니다.
func WithReadTimeout(d time.Duration) Option {
	return func(s *Server) {
		s.readTimeout = d
	}
}

// WithWriteTimeout sets the write timeout.
// WithWriteTimeout은 쓰기 타임아웃을 설정합니다.
func WithWriteTimeout(d time.Duration) Option {
	return func(s *Server) {
		s.writeTimeout = d
	}
}

// WithKeepAliveTimeout sets the Keep-Alive timeout.
// WithKeepAliveTimeout은 Keep-Alive 타임아웃을 설정합니다.
func WithKeepAliveTimeout(d time.Duration) Option {
	return func(s *Server) {
		s.keepAliveTimeout = d
	}
}

// NewServer creates a new Server.
// NewServer는 새로운 Server를 생성합니다.
func NewServer(e *engine.Engine, opts ...Option) *Server {
	s := &Server{
		Engine:           e,
		keepAliveTimeout: 30 * time.Second, // Default
		readTimeout:      10 * time.Second,
		writeTimeout:     10 * time.Second,
		maxConns:         0, // Default: Unlimited
	}

	for _, opt := range opts {
		opt(s)
	}

	return s
}

// Serve starts the netpoll event loop to handle incoming requests.
// It uses SO_REUSEPORT for improved multi-process/thread binding performance.
// Serve는 netpoll 이벤트 루프를 시작하여 들어오는 요청을 처리합니다.
// SO_REUSEPORT를 사용하여 다중 프로세스/스레드 바인딩 성능을 향상시킵니다.
func (s *Server) Serve(addr string) error {
	l, err := reuseport.Listen("tcp", addr)
	if err != nil {
		return err
	}

	listener, err := netpoll.ConvertListener(l)
	if err != nil {
		return err
	}

	log.Printf("Server listening on %s (MaxConns: %d)", addr, s.maxConns)

	opts := []netpoll.Option{
		netpoll.WithIdleTimeout(s.keepAliveTimeout),
		netpoll.WithOnPrepare(func(conn netpoll.Connection) context.Context {
			// Connection Limiter
			current := atomic.AddInt32(&s.connsCount, 1)
			if s.maxConns > 0 && current > s.maxConns {
				atomic.AddInt32(&s.connsCount, -1)
				conn.Close()
				return nil
			}

			if err := conn.SetReadTimeout(s.readTimeout); err != nil {
				log.Printf("Failed to set read timeout: %v. Closing connection.", err)
				conn.Close()
				return nil
			}
			if s.writeTimeout > 0 {
				conn.SetWriteTimeout(s.writeTimeout)
			}

			// Optimization: Use ConnectionState as Context directly (Zero-Alloc)
			// ConnectionState implements context.Context and manages its own cancellation.
			state := engine.NewConnectionState(s.readTimeout)
			return state
		}),
		netpoll.WithOnDisconnect(func(ctx context.Context, connection netpoll.Connection) {
			// Decrement connection count
			atomic.AddInt32(&s.connsCount, -1)

			// Retrieve and release ConnectionState resources
			// ConnectionState is the context itself
			state, ok := ctx.(*engine.ConnectionState)
			if !ok {
				// Fallback if context was wrapped or something unexpected
				val := ctx.Value(engine.CtxKeyConnectionState)
				if val == nil {
					log.Printf("Warning: ConnectionState not found in context during disconnect")
					return
				}
				state, ok = val.(*engine.ConnectionState)
				if !ok {
					log.Printf("Warning: Invalid ConnectionState type in context during disconnect")
					return
				}
			}

			state.Cancel() // Cancels context on connection disconnect.

			// Return buffers to the Engine's pool and state to global pool
			s.Engine.ReleaseConnectionState(state)
		}),
	}

	// OnRequest callback invokes the Engine's ServeConn method.
	// OnRequest 콜백은 Engine의 ServeConn 메서드를 호출합니다.
	eventLoop, err := netpoll.NewEventLoop(s.Engine.ServeConn, opts...)
	if err != nil {
		return err
	}
	s.eventLoop = eventLoop

	return eventLoop.Serve(listener)
}

// Shutdown gracefully shuts down the server.
// Shutdown은 서버를 정상적으로(gracefully) 종료합니다.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.eventLoop == nil {
		return nil
	}
	return s.eventLoop.Shutdown(ctx)
}
