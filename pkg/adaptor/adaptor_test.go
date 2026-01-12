package adaptor

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"testing"

	"github.com/DevNewbie1826/hon/pkg/appcontext"
	"github.com/cloudwego/netpoll"
)

// MockConn implements netpoll.Connection partially for testing
type MockConn struct {
	net.Conn
	buf *bytes.Buffer
}

func (m *MockConn) Reader() netpoll.Reader { return nil }
func (m *MockConn) Writer() netpoll.Writer { return netpoll.NewWriter(m.buf) }
func (m *MockConn) IsActive() bool         { return true }
func (m *MockConn) Close() error           { return nil }
func (m *MockConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}
}
func (m *MockConn) Read(p []byte) (int, error) { return 0, io.EOF }

func TestResponseWriter_Write(t *testing.T) {
	buf := new(bytes.Buffer)
	// Inject a bufio.Writer writing to our buffer
	bw := bufio.NewWriter(buf)
	ctx := appcontext.NewRequestContext(nil, context.Background(), nil, bw)

	rw := NewResponseWriter(ctx, nil)

	// Test Header Setting
	rw.Header().Set("X-Custom", "Foo")
	rw.WriteHeader(http.StatusCreated)

	// Test Write
	n, err := rw.Write([]byte("Hello"))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != 5 {
		t.Errorf("Expected 5 bytes written, got %d", n)
	}

	rw.Flush() // Flush to buffer

	resp := buf.String()
	if !contains(resp, "HTTP/1.1 201 Created") {
		t.Errorf("Status code missing")
	}
	if !contains(resp, "X-Custom: Foo") {
		t.Errorf("Header missing")
	}
	if !contains(resp, "Hello") {
		t.Errorf("Body missing")
	}

	rw.Release()
}

func TestResponseWriter_Chunked(t *testing.T) {
	buf := new(bytes.Buffer)
	bw := bufio.NewWriter(buf)
	ctx := appcontext.NewRequestContext(nil, context.Background(), nil, bw)

	rw := NewResponseWriter(ctx, nil)
	// Do NOT set Content-Length -> Triggers Chunked

	rw.Write([]byte("Chunk1"))
	rw.Write([]byte("Chunk2"))
	rw.EndResponse()

	resp := buf.String()
	if !contains(resp, "Transfer-Encoding: chunked") {
		t.Errorf("Transfer-Encoding header missing")
	}
	// "6\r\nChunk1\r\n"
	if !contains(resp, "6\r\nChunk1\r\n") {
		t.Errorf("Chunk1 format incorrect")
	}
	// End "0\r\n\r\n"
	if !contains(resp, "0\r\n\r\n") {
		t.Errorf("Chunk end missing")
	}
	rw.Release()
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && bytes.Contains([]byte(s), []byte(substr))
}
