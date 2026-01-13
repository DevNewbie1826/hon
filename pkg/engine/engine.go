package engine

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DevNewbie1826/hon/pkg/adaptor"
	"github.com/DevNewbie1826/hon/pkg/appcontext"
	"github.com/DevNewbie1826/hon/pkg/engine/parser"
	"github.com/cloudwego/netpoll"
)

const MaxDrainSize = 64 * 1024 // 64KB

var connectionStatePool = sync.Pool{
	New: func() any {
		return new(ConnectionState)
	},
}

type ConnectionState struct {
	Reader      *bufio.Reader
	Writer      *bufio.Writer
	ReadHandler appcontext.ReadHandler
	CancelFunc  context.CancelFunc
	ReadTimeout time.Duration
	Processing  atomic.Bool
	refCount    int32 // Reference count for safe resource release
	done        chan struct{}
	err         error
}

func NewConnectionState(readTimeout time.Duration) *ConnectionState {
	s := connectionStatePool.Get().(*ConnectionState)
	s.ReadTimeout = readTimeout
	s.refCount = 1 // Initial reference held by the connection (OnPrepare)
	s.done = make(chan struct{})
	s.err = nil
	return s
}

func (s *ConnectionState) Reset() {
	// Note: This method is only called when refCount reaches 0.
	// At that point, no other goroutine should be accessing this state,
	// so using non-atomic assignments is safe.
	s.Reader = nil
	s.Writer = nil
	s.CancelFunc = nil
	s.ReadHandler = nil
	s.Processing.Store(false)
	s.ReadTimeout = 0
	s.refCount = 0
	s.done = nil
	s.err = nil
}

// Deadline implements context.Context
func (s *ConnectionState) Deadline() (deadline time.Time, ok bool) {
	return
}

// Done implements context.Context
func (s *ConnectionState) Done() <-chan struct{} {
	return s.done
}

// Err implements context.Context
func (s *ConnectionState) Err() error {
	return s.err
}

// Value implements context.Context
func (s *ConnectionState) Value(key any) any {
	if key == CtxKeyConnectionState {
		return s
	}
	return nil
}

// Cancel closes the done channel, simulating context cancellation.
func (s *ConnectionState) Cancel() {
	if s.err == nil {
		s.err = context.Canceled
		close(s.done)
	}
}

var CtxKeyConnectionState = struct{}{}

type Option func(*Engine)

func WithRequestTimeout(d time.Duration) Option {
	return func(e *Engine) {
		e.requestTimeout = d
	}
}

func WithMaxDrainSize(size int64) Option {
	return func(e *Engine) {
		e.maxDrainSize = size
	}
}

func WithBufferSize(size int) Option {
	return func(e *Engine) {
		e.bufferSize = size
	}
}

type Engine struct {
	Handler        http.Handler
	requestTimeout time.Duration
	maxDrainSize   int64
	bufferSize     int

	readerPool sync.Pool
	writerPool sync.Pool
}

func NewEngine(handler http.Handler, opts ...Option) *Engine {
	e := &Engine{
		Handler:      handler,
		maxDrainSize: MaxDrainSize,
		bufferSize:   4096,
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

// AcquireConnectionState increments the reference count.
// Must be called when entering a goroutine that uses the state.
func (e *Engine) AcquireConnectionState(s *ConnectionState) {
	atomic.AddInt32(&s.refCount, 1)
}

// ReleaseConnectionState decrements the reference count.
// If count reaches 0, resources are returned to the pool.
// Must be called when leaving a goroutine that uses the state.
func (e *Engine) ReleaseConnectionState(s *ConnectionState) {
	if atomic.AddInt32(&s.refCount, -1) == 0 {
		if s.Reader != nil {
			e.readerPool.Put(s.Reader)
		}
		if s.Writer != nil {
			e.writerPool.Put(s.Writer)
		}
		s.Reset()
		connectionStatePool.Put(s)
	}
}

func (e *Engine) ServeConn(ctx context.Context, conn netpoll.Connection) error {
	stateVal := ctx.Value(CtxKeyConnectionState)
	if stateVal == nil {
		return errors.New("connection state not found")
	}
	state := stateVal.(*ConnectionState)

	e.AcquireConnectionState(state)
	defer e.ReleaseConnectionState(state)

	// Top-Level Panic Recovery for this connection
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Critical Panic] Recovered in ServeConn: %v\n%s", r, debug.Stack())
			conn.Close()
			// State will be released by the outer defer
		}
	}()

	if !state.Processing.CompareAndSwap(false, true) {
		return nil
	}

	if state.Reader == nil {
		state.Reader = e.readerPool.Get().(*bufio.Reader)
		state.Reader.Reset(conn)
	}
	if state.Writer == nil {
		state.Writer = e.writerPool.Get().(*bufio.Writer)
		state.Writer.Reset(conn)
	}

	if state.ReadHandler != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[Panic] Recovered in ReadHandler: %v\n%s", r, debug.Stack())
					conn.Close()
				}
				state.Processing.Store(false)
			}()
			rw := bufio.NewReadWriter(state.Reader, state.Writer)
			if err := state.ReadHandler(conn, rw); err != nil {
				if err != io.EOF && !strings.Contains(err.Error(), "EOF") {
					log.Printf("ReadHandler error: %v", err)
				}
				conn.Close()
			}
		}()
		return nil
	}

	e.serveHTTP(ctx, conn, state)
	return nil
}

func (e *Engine) serveHTTP(ctx context.Context, conn netpoll.Connection, state *ConnectionState) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Panic] Recovered in serveHTTP: %v\n%s", r, debug.Stack())
			conn.Close()
			state.Processing.Store(false)
		}
	}()

	for {
		// Yield to other goroutines to prevent starvation under heavy load
		runtime.Gosched()

		if !conn.IsActive() {
			state.Processing.Store(false)
			return
		}

		// Optimization: Check if we have enough data to parse a request
		if state.Reader.Buffered() == 0 {
			r := conn.Reader()
			if r != nil {
				if r.Len() == 0 {
					state.Processing.Store(false)
					return
				}
				peekBuf, _ := r.Peek(r.Len())
				if !parser.CheckRequest(peekBuf).Complete {
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
		requestContext.Release()

		if err != nil {
			if err != io.EOF {
				conn.Close()
			}
			state.Processing.Store(false)
			return
		}

		if hijacked {
			_ = conn.SetReadDeadline(time.Time{})
			_ = conn.SetWriteDeadline(time.Time{})

			if state.ReadHandler != nil {
				state.Processing.Store(false)
				return
			}
			<-ctx.Done()
			state.Processing.Store(false)
			return
		}

		// Drain body for keep-alive
		if req.Body != nil {
			_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			n, _ := io.Copy(io.Discard, io.LimitReader(req.Body, e.maxDrainSize+1))
			_ = req.Body.Close()
			_ = conn.SetReadDeadline(time.Time{})
			if n > e.maxDrainSize {
				req.Close = true
			}
		}

		if req.Close || req.Header.Get("Connection") == "close" {
			conn.Close()
			state.Processing.Store(false)
			return
		}

		// Restore KA Deadlines
		_ = conn.SetReadDeadline(time.Time{})
		if state.ReadTimeout > 0 {
			_ = conn.SetReadTimeout(state.ReadTimeout)
		}

		if state.Reader.Buffered() > 0 {
			continue
		}

		// Double-Check Locking
		state.Processing.Store(false)
		hasData := state.Reader.Buffered() > 0
		if !hasData {
			if r := conn.Reader(); r != nil && r.Len() > 0 {
				hasData = true
			}
		}

		if hasData {
			if state.Processing.CompareAndSwap(false, true) {
				continue
			}
			return
		}
		return
	}
}

func (e *Engine) handleRequest(ctx *appcontext.RequestContext) (*http.Request, bool, error) {
	req, err := adaptor.GetRequest(ctx)
	if err != nil {
		return nil, false, err
	}

	respWriter := adaptor.NewResponseWriter(ctx, req)
	defer respWriter.Release()

	if e.requestTimeout > 0 {
		timeoutCtx, cancel := context.WithTimeout(req.Context(), e.requestTimeout)
		req = req.WithContext(timeoutCtx)
		defer cancel()
	}

	var panicked bool
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
				log.Printf("[Panic] Recovered in handler: %v\n%s", r, debug.Stack())
				if !respWriter.HeaderSent() {
					respWriter.WriteHeader(http.StatusInternalServerError)
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
