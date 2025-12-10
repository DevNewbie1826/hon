package appcontext

import (
	"bufio"
	"context"
	"sync"

	"github.com/cloudwego/netpoll"
)

// ReadHandler is a function type for handling custom connection reads (e.g., WebSocket).
type ReadHandler func(conn netpoll.Connection, rw *bufio.ReadWriter) error

// RequestContext holds all necessary information during the lifecycle of an HTTP request.
// RequestContext는 HTTP 요청의 전체 생명주기 동안 필요한 모든 정보를 담습니다.
type RequestContext struct {
	conn             netpoll.Connection
	req              context.Context // Parent context. // 부모 컨텍스트.
	reader           *bufio.Reader
	writer           *bufio.Writer
	onSetReadHandler func(ReadHandler)
}

// pool recycles RequestContext objects to reduce GC pressure.
// pool은 가비지 컬렉션(GC) 부하를 줄이기 위해 RequestContext 객체를 재활용합니다.
var pool = sync.Pool{
	New: func() any {
		return new(RequestContext)
	},
}

// NewRequestContext retrieves and initializes a RequestContext from the pool.
// NewRequestContext는 풀에서 RequestContext를 가져와 초기화합니다.
func NewRequestContext(conn netpoll.Connection, parent context.Context, reader *bufio.Reader, writer *bufio.Writer) *RequestContext {
	c := pool.Get().(*RequestContext)
	c.conn = conn
	c.req = parent
	c.reader = reader
	c.writer = writer
	return c
}

// SetReadHandler sets the custom read handler for the connection.
func (c *RequestContext) SetReadHandler(h ReadHandler) {
	if c.onSetReadHandler != nil {
		c.onSetReadHandler(h)
	}
}

// SetOnSetReadHandler sets the callback to be called when SetReadHandler is invoked.
func (c *RequestContext) SetOnSetReadHandler(cb func(ReadHandler)) {
	c.onSetReadHandler = cb
}

// Release returns the RequestContext to the pool for reuse.
// Release는 RequestContext를 풀에 반환하여 재사용할 수 있도록 합니다.
func (c *RequestContext) Release() {
	c.reset()
	pool.Put(c)
}

// reset initializes the fields of RequestContext.
// reset은 RequestContext의 필드를 초기화합니다.
func (c *RequestContext) reset() {
	c.conn = nil
	c.req = nil
	c.reader = nil
	c.writer = nil
	c.onSetReadHandler = nil
}

// Conn returns the netpoll.Connection.
// Conn은 netpoll.Connection을 반환합니다.
func (c *RequestContext) Conn() netpoll.Connection {
	return c.conn
}

// Req returns the parent context.
// Req는 부모 컨텍스트를 반환합니다.
func (c *RequestContext) Req() context.Context {
	return c.req
}

// GetReader returns a reusable bufio.Reader.
// GetReader는 재사용 가능한 bufio.Reader를 반환합니다.
func (c *RequestContext) GetReader() *bufio.Reader {
	return c.reader
}

// GetWriter returns a reusable bufio.Writer.
// GetWriter는 재사용 가능한 bufio.Writer를 반환합니다.
func (c *RequestContext) GetWriter() *bufio.Writer {
	return c.writer
}
