package appcontext

import (
	"bufio"
	"context"
	"sync"

	"github.com/cloudwego/netpoll"
)

// ReadHandler is a function type for handling custom connection reads (e.g., WebSocket).
// ReadHandler는 사용자 정의 연결 읽기(예: WebSocket)를 처리하기 위한 함수 타입입니다.
type ReadHandler func(conn netpoll.Connection, rw *bufio.ReadWriter) error

// RequestContext holds all necessary information during the lifecycle of an HTTP request.
// RequestContext는 HTTP 요청의 전체 생명주기 동안 필요한 모든 정보를 담습니다.
type RequestContext struct {
	conn             netpoll.Connection      // The underlying netpoll connection. // 기반 netpoll 연결입니다.
	req              context.Context         // Parent context for the request. // 요청에 대한 부모 컨텍스트입니다.
	reader           *bufio.Reader           // Reusable buffered reader for the connection. // 연결을 위한 재사용 가능한 버퍼링된 리더입니다.
	writer           *bufio.Writer           // Reusable buffered writer for the connection. // 연결을 위한 재사용 가능한 버퍼링된 라이터입니다.
	onSetReadHandler func(ReadHandler)       // Callback for when a custom read handler is set. // 사용자 정의 읽기 핸들러가 설정될 때 호출되는 콜백입니다.
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
// SetReadHandler는 연결에 대한 사용자 정의 읽기 핸들러를 설정합니다.
func (c *RequestContext) SetReadHandler(h ReadHandler) {
	if c.onSetReadHandler != nil {
		c.onSetReadHandler(h)
	}
}

// SetOnSetReadHandler sets the callback to be called when SetReadHandler is invoked.
// SetOnSetReadHandler는 SetReadHandler가 호출될 때 실행될 콜백을 설정합니다.
func (c *RequestContext) SetOnSetReadHandler(cb func(ReadHandler)) {
	c.onSetReadHandler = cb
}

// Release returns the RequestContext to the pool for reuse.
// Release는 RequestContext를 풀에 반환하여 재사용할 수 있도록 합니다.
func (c *RequestContext) Release() {
	c.reset()
	pool.Put(c)
}

// reset initializes the fields of RequestContext to their zero values for reuse.
// reset은 RequestContext의 필드를 재사용을 위해 초기값으로 설정합니다.
func (c *RequestContext) reset() {
	c.conn = nil
	c.req = nil
	c.reader = nil
	c.writer = nil
	c.onSetReadHandler = nil
}

// Conn returns the netpoll.Connection associated with this context.
// Conn은 이 컨텍스트와 연결된 netpoll.Connection을 반환합니다.
func (c *RequestContext) Conn() netpoll.Connection {
	return c.conn
}

// Req returns the parent context for the request.
// Req는 요청에 대한 부모 컨텍스트를 반환합니다.
func (c *RequestContext) Req() context.Context {
	return c.req
}

// GetReader returns the reusable bufio.Reader for the connection.
// GetReader는 연결에 대한 재사용 가능한 bufio.Reader를 반환합니다.
func (c *RequestContext) GetReader() *bufio.Reader {
	return c.reader
}

// GetWriter returns the reusable bufio.Writer for the connection.
// GetWriter는 연결에 대한 재사용 가능한 bufio.Writer를 반환합니다.
func (c *RequestContext) GetWriter() *bufio.Writer {
	return c.writer
}
