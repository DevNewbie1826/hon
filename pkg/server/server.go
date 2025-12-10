package server

import (
	"context"
	"log"
	"time"

	"github.com/DevNewbie1826/hon/pkg/engine"
	"github.com/cloudwego/netpoll"
	"github.com/libp2p/go-reuseport"
)

// Server is the top-level structure for the netpoll server.
// Server는 netpoll 서버의 최상위 구조체입니다.
type Server struct {
	Engine           *engine.Engine
	eventLoop        netpoll.EventLoop
	keepAliveTimeout time.Duration
	readTimeout      time.Duration
	writeTimeout     time.Duration
}

// Option is a function type for configuring the Server.
// Option은 서버 설정을 위한 함수 타입입니다.
type Option func(*Server)

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

	log.Printf("Server listening on %s", addr)

	opts := []netpoll.Option{
		netpoll.WithIdleTimeout(s.keepAliveTimeout),
		netpoll.WithOnPrepare(func(conn netpoll.Connection) context.Context {
			conn.SetReadTimeout(s.readTimeout) // nolint:errcheck
			if s.writeTimeout > 0 {
				conn.SetWriteTimeout(s.writeTimeout)
			}

			ctx, cancel := context.WithCancel(context.Background())

			// Initialize ConnectionState and inject into context
			state := &engine.ConnectionState{
				ReadTimeout: s.readTimeout,
				CancelFunc:  cancel,
			}
			ctx = context.WithValue(ctx, engine.CtxKeyConnectionState, state)

			return ctx
		}),
		netpoll.WithOnDisconnect(func(ctx context.Context, connection netpoll.Connection) {
			// Retrieve and release ConnectionState resources
			if val := ctx.Value(engine.CtxKeyConnectionState); val != nil {
				if state, ok := val.(*engine.ConnectionState); ok {
					if state.CancelFunc != nil {
						state.CancelFunc() // Cancels context on connection disconnect.
					}
					state.Release()
				}
			}
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


