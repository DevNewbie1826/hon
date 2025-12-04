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
	// Acquire a bufio.Reader for the life of the connection
	// 연결 수명 동안 사용할 bufio.Reader를 획득합니다.
	reader := bufReaderPool.Get().(*bufio.Reader)
	reader.Reset(conn)
	defer bufReaderPool.Put(reader)

	// Acquire a bufio.Writer for the life of the connection
	// 연결 수명 동안 사용할 bufio.Writer를 획득합니다.
	writer := bufWriterPool.Get().(*bufio.Writer)
	writer.Reset(conn)
	defer bufWriterPool.Put(writer)

	for {
		requestContext := appcontext.NewRequestContext(conn, ctx, reader, writer)

		req, hijacked, err := e.handleRequest(requestContext)

		if err != nil {
			requestContext.Release()
			return err
		}

		if hijacked {
			// If hijacked, the reader is now owned by the hijacker (or lost).
			// We should probably NOT put it back to the pool if we can't guarantee its state,
			// but here we deferred Put.
			// However, Hijack() in adaptor returns a new bufio.ReadWriter.
			// The original reader (this one) is still attached to the conn.
			// If hijacked, we stop this loop.
			<-ctx.Done()
			return nil
		}

		// Body Draining: Read and discard remaining body data for the next request.
		// Body Draining: 다음 요청을 위해 남은 바디 데이터를 읽어서 버립니다.
		if req.Body != nil {
			// Limit the amount of body we drain to prevent resource exhaustion from large unread bodies.
			// 만약 핸들러가 바디를 다 읽지 않았고, 남은 바디가 MaxDrainSize보다 크다면
			// 연결을 끊어서 불필요한 리소스 소모를 방지하고 다음 요청의 오염을 막습니다.
			n, _ := io.Copy(io.Discard, io.LimitReader(req.Body, MaxDrainSize+1))
			_ = req.Body.Close()

			if n > MaxDrainSize {
				// If we tried to drain more than MaxDrainSize, it means the body was too large
				// and wasn't fully consumed by the handler. Force close the connection.
				req.Close = true
			}
		}

		requestContext.Release()

		// Keep-alive logic: Decides whether to close the connection based on the request.
		// keep-alive 로직: 요청에 따라 연결을 닫을지 결정합니다.
		if req.Close || req.Header.Get("Connection") == "close" {
			return nil
		}
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
				respWriter.WriteHeader(http.StatusInternalServerError)
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
