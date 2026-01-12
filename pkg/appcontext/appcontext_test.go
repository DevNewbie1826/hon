package appcontext

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/cloudwego/netpoll"
)

// MockConnection implements netpoll.Connection for testing
type MockConnection struct {
	netpoll.Connection
	readBuf  bytes.Buffer
	writeBuf bytes.Buffer
	closed   bool
	// Mocking SetOnRequest and AddCloseCallback to avoid nil panics if called
	onRequestCalled bool
	onCloseCalled   bool
}

func (m *MockConnection) Read(b []byte) (n int, err error)     { return m.readBuf.Read(b) }
func (m *MockConnection) Write(b []byte) (n int, err error)    { return m.writeBuf.Write(b) }
func (m *MockConnection) Close() error                         { m.closed = true; return nil }
func (m *MockConnection) IsActive() bool                       { return !m.closed }
func (m *MockConnection) SetReadDeadline(t time.Time) error    { return nil }
func (m *MockConnection) SetWriteDeadline(t time.Time) error   { return nil }
func (m *MockConnection) SetReadTimeout(t time.Duration) error { return nil }
func (m *MockConnection) SetIdleTimeout(t time.Duration) error { return nil }
func (m *MockConnection) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8080}
}
func (m *MockConnection) Reader() netpoll.Reader { return nil } // Not used by appcontext
func (m *MockConnection) SetOnRequest(on netpoll.OnRequest) error {
	m.onRequestCalled = true
	return nil
}
func (m *MockConnection) AddCloseCallback(callback netpoll.CloseCallback) error {
	m.onCloseCalled = true
	return nil
}

func TestNewRequestContext_Release_Pool(t *testing.T) {
	conn := &MockConnection{}
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	parentCtx := context.Background()

	// Get first context
	ctx1 := NewRequestContext(conn, parentCtx, reader, writer)
	if ctx1 == nil {
		t.Fatal("Expected RequestContext, got nil")
	}

	// Release it
	ctx1.Release()

	// Get second context - should be the same object
	ctx2 := NewRequestContext(conn, parentCtx, reader, writer)
	if ctx2 == nil {
		t.Fatal("Expected RequestContext, got nil")
	}

	if ctx1 != ctx2 {
		t.Errorf("Expected ctx1 and ctx2 to be the same object due to pooling")
	}

	// Verify fields are reset
	if ctx2.conn != conn || ctx2.req != parentCtx || ctx2.reader != reader || ctx2.writer != writer {
		t.Errorf("Expected fields to be reinitialized, but found lingering data")
	}
	if ctx2.onSetReadHandler != nil {
		t.Errorf("Expected onSetReadHandler to be nil after reset")
	}

	ctx2.Release()
}

func TestRequestContext_Accessors(t *testing.T) {
	conn := &MockConnection{}
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	parentCtx := context.WithValue(context.Background(), "key", "value")

	ctx := NewRequestContext(conn, parentCtx, reader, writer)
	defer ctx.Release()

	if ctx.Conn() != conn {
		t.Errorf("Expected Conn() to return mock conn, got %v", ctx.Conn())
	}
	if ctx.Req() != parentCtx {
		t.Errorf("Expected Req() to return parentCtx, got %v", ctx.Req())
	}
	if ctx.GetReader() != reader {
		t.Errorf("Expected GetReader() to return mock reader, got %v", ctx.GetReader())
	}
	if ctx.GetWriter() != writer {
		t.Errorf("Expected GetWriter() to return mock writer, got %v", ctx.GetWriter())
	}
}

func TestRequestContext_SetReadHandler(t *testing.T) {
	conn := &MockConnection{}
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	parentCtx := context.Background()

	ctx := NewRequestContext(conn, parentCtx, reader, writer)
	defer ctx.Release()

	handlerCalled := false
	testHandler := func(c netpoll.Connection, rw *bufio.ReadWriter) error {
		handlerCalled = true
		return nil
	}

	var capturedHandler ReadHandler
	ctx.SetOnSetReadHandler(func(h ReadHandler) {
		capturedHandler = h
	})

	ctx.SetReadHandler(testHandler)

	if capturedHandler == nil {
		t.Fatal("Expected capturedHandler to be set")
	}

	// Simulate engine calling the captured handler
	capturedHandler(conn, bufio.NewReadWriter(reader, writer))

	if !handlerCalled {
		t.Error("Expected testHandler to be called via capturedHandler")
	}
}
