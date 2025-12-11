package engine

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
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
}

func (m *MockConnection) Read(b []byte) (n int, err error) {
	return m.readBuf.Read(b)
}

func (m *MockConnection) Write(b []byte) (n int, err error) {
	return m.writeBuf.Write(b)
}

func (m *MockConnection) Close() error {
	m.closed = true
	return nil
}

func (m *MockConnection) IsActive() bool {
	return !m.closed
}

func (m *MockConnection) SetReadDeadline(t time.Time) error { return nil }
func (m *MockConnection) SetWriteDeadline(t time.Time) error { return nil }
func (m *MockConnection) SetReadTimeout(t time.Duration) error { return nil }
func (m *MockConnection) RemoteAddr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8080} }
// Reader returns a valid reader so conn.Reader().Len() checks works in ServeConn double-check lock
func (m *MockConnection) Reader() netpoll.Reader {
    // For simplicity in this mock, we can return nil or a dummy.
    // The engine checks `if r := conn.Reader(); r != nil && r.Len() > 0`.
    // If we want to test the double-check lock, we need a real implementation, 
    // but for basic tests, nil is fine as long as we don't rely on that specific path.
    return nil 
}


// fillRequest writes a simple HTTP request to the read buffer
func (m *MockConnection) fillRequest(method, path, body string) {
	req := fmt.Sprintf("%s %s HTTP/1.1\r\nHost: localhost\r\nContent-Length: %d\r\n\r\n%s", method, path, len(body), body)
	m.readBuf.WriteString(req)
}

func TestEngine_ServeConn_Basic(t *testing.T) {
	// 1. Setup Engine with a simple handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("Expected method GET, got %s", r.Method)
		}
		if r.URL.Path != "/" {
			t.Errorf("Expected path /, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Hello, World!"))
	})
	eng := NewEngine(handler)

	// 2. Setup Mock Connection
	conn := &MockConnection{}
	conn.fillRequest("GET", "/", "")

	// 3. Setup Context with ConnectionState (Required by ServeConn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := NewConnectionState(time.Second, cancel)
	ctx = context.WithValue(ctx, CtxKeyConnectionState, state)

	// 4. Call ServeConn
	// Since ServeConn runs the handler synchronously in this design (except for the loop),
	// it should process the request and return.
	err := eng.ServeConn(ctx, conn)
	if err != nil {
		t.Fatalf("ServeConn failed: %v", err)
	}

	// 5. Verify Response
	output := conn.writeBuf.String()
	if !strings.Contains(output, "HTTP/1.1 200 OK") {
		t.Errorf("Expected 200 OK, got output:\n%s", output)
	}
	if !strings.Contains(output, "Hello, World!") {
		t.Errorf("Expected body 'Hello, World!', got output:\n%s", output)
	}
}

func TestEngine_ServeConn_PanicRecovery(t *testing.T) {
	// 1. Setup Engine with a panicking handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("something went wrong")
	})
	eng := NewEngine(handler)

	// 2. Setup Mock Connection
	conn := &MockConnection{}
	conn.fillRequest("GET", "/panic", "")

	// 3. Setup Context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := NewConnectionState(time.Second, cancel)
	ctx = context.WithValue(ctx, CtxKeyConnectionState, state)

	// 4. Call ServeConn
	// It should recover from panic and NOT crash the test.
	// It should also try to write a 500 response if headers weren't sent.
	err := eng.ServeConn(ctx, conn)
	if err != nil {
		// ServeConn might return nil even on panic handled internally, 
		// or it might close connection.
	}

	// 5. Verify 500 Response
	output := conn.writeBuf.String()
	if !strings.Contains(output, "HTTP/1.1 500 Internal Server Error") {
		t.Errorf("Expected 500 Internal Server Error, got output:\n%s", output)
	}
}

func TestEngine_ServeConn_KeepAlive(t *testing.T) {
	// 1. Setup Engine
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Response " + id))
	})
	eng := NewEngine(handler)

	// 2. Setup Mock Connection with pipelined requests
	conn := &MockConnection{}
	// Request 1
	conn.readBuf.WriteString("GET /?id=1 HTTP/1.1\r\nHost: localhost\r\n\r\n")
	// Request 2
	conn.readBuf.WriteString("GET /?id=2 HTTP/1.1\r\nHost: localhost\r\n\r\n")

	// 3. Setup Context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := NewConnectionState(time.Second, cancel)
	ctx = context.WithValue(ctx, CtxKeyConnectionState, state)

	// 4. Call ServeConn
	// It should loop and process both requests because buffer has data.
	err := eng.ServeConn(ctx, conn)
	if err != nil {
		t.Fatalf("ServeConn failed: %v", err)
	}

	// 5. Verify Responses
	output := conn.writeBuf.String()
	
	// Check for Response 1
	if !strings.Contains(output, "Response 1") {
		t.Errorf("Expected Response 1, got output:\n%s", output)
	}
	
	// Check for Response 2
	if !strings.Contains(output, "Response 2") {
		t.Errorf("Expected Response 2, got output:\n%s", output)
	}

	// Verify order roughly (simple check)
	idx1 := strings.Index(output, "Response 1")
	idx2 := strings.Index(output, "Response 2")
	if idx1 == -1 || idx2 == -1 || idx1 > idx2 {
		t.Errorf("Responses out of order or missing. Index 1: %d, Index 2: %d", idx1, idx2)
	}
}

func TestEngine_MaxDrainSize(t *testing.T) {
	// 1. Setup Engine with handler that does NOT read body
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// MaxDrainSize is 64KB constant. We can't change constant for test easily,
	// but we can pass an Option to NewEngine to override it?
	// NewEngine uses opts to configure maxDrainSize.
	// Let's set a small maxDrainSize for testing.
	eng := NewEngine(handler, WithMaxDrainSize(10)) // 10 bytes limit

	// 2. Setup Mock Connection with huge body
	conn := &MockConnection{}
	body := "This body is definitely longer than 10 bytes."
	conn.fillRequest("POST", "/", body)

	// 3. Setup Context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := NewConnectionState(time.Second, cancel)
	ctx = context.WithValue(ctx, CtxKeyConnectionState, state)

	// 4. Call ServeConn
	err := eng.ServeConn(ctx, conn)
	if err != nil {
		// ServeConn might return nil even if connection is closed inside
	}

	// 5. Verify Connection Closed
	// Since body > 10 bytes and handler didn't read it, engine tries to drain.
	// Draining > 10 bytes should trigger req.Close = true and subsequently conn.Close()
	if !conn.closed {
		t.Error("Connection should be closed due to MaxDrainSize exceeded")
	}
}

func TestEngine_RequestTimeout(t *testing.T) {
	// 1. Setup Engine with short timeout
	timeout := 10 * time.Millisecond
	timeoutOccurred := make(chan bool, 1)
	
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate long work
		select {
		case <-time.After(100 * time.Millisecond):
			timeoutOccurred <- false
		case <-r.Context().Done():
			timeoutOccurred <- true
		}
		w.WriteHeader(http.StatusOK)
	})
	eng := NewEngine(handler, WithRequestTimeout(timeout))

	// 2. Setup Mock Connection
	conn := &MockConnection{}
	conn.fillRequest("GET", "/", "")

	// 3. Setup Context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := NewConnectionState(time.Second, cancel)
	ctx = context.WithValue(ctx, CtxKeyConnectionState, state)

	// 4. Call ServeConn
	_ = eng.ServeConn(ctx, conn)

	// 5. Verify Timeout
	select {
	case result := <-timeoutOccurred:
		if !result {
			t.Error("Handler context should have been cancelled (timeout)")
		}
	case <-time.After(1 * time.Second):
		t.Error("Test timed out waiting for handler")
	}
}

// Helper for string contains check
