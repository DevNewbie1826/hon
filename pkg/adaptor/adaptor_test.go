package adaptor

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
    "bytes"
    "strings"

	"github.com/DevNewbie1826/hon/pkg/appcontext"
	"github.com/cloudwego/netpoll"
)

// MockConnection implements netpoll.Connection for testing
type MockConnection struct {
	netpoll.Connection
	readBuf  bytes.Buffer
	writeBuf bytes.Buffer
}

func (m *MockConnection) Write(b []byte) (n int, err error) {
	return m.writeBuf.Write(b)
}

func (m *MockConnection) RemoteAddr() net.Addr {
	return nil
}

func (m *MockConnection) Read(b []byte) (n int, err error) {
	return m.readBuf.Read(b)
}

func (m *MockConnection) Close() error {
	return nil
}

func (m *MockConnection) IsActive() bool {
	return true
}

func (m *MockConnection) SetReadDeadline(t time.Time) error { return nil }
func (m *MockConnection) SetWriteDeadline(t time.Time) error { return nil }
func (m *MockConnection) SetReadTimeout(t time.Duration) error { return nil }
func (m *MockConnection) SetIdleTimeout(t time.Duration) error { return nil }
func (m *MockConnection) SetOnRequest(on netpoll.OnRequest) error { return nil }
func (m *MockConnection) AddCloseCallback(callback netpoll.CloseCallback) error { return nil }

func TestResponseWriter_Write_ZeroByte(t *testing.T) {
    conn := &MockConnection{}
    reader := bufio.NewReader(conn)
    writer := bufio.NewWriter(conn)
    ctx := appcontext.NewRequestContext(conn, context.Background(), reader, writer)
    req, _ := http.NewRequest("GET", "/", nil)

    w := NewResponseWriter(ctx, req)
    
    // Force Chunked
    w.Header().Set("Transfer-Encoding", "chunked") 
    // Content-Length not set implies chunked in ensureHeaderSent logic if body is written
    
    // Write 0 bytes
    n, err := w.Write([]byte{})
    if err != nil {
        t.Fatalf("Write failed: %v", err)
    }
    if n != 0 {
        t.Errorf("Expected 0 bytes written, got %d", n)
    }
    
    w.Flush()
    
    // Check output. 
    output := conn.writeBuf.String()
    
    // 1. It should NOT contain the chunk termination sequence "0\r\n\r\n"
    // Since we wrote 0 bytes, no chunk should be written, and definitely not the end chunk (until EndResponse)
    if strings.Contains(output, "0\r\n\r\n") {
        t.Errorf("Premature chunked termination detected in output: %q", output)
    }

    // 2. It should NOT have duplicate Transfer-Encoding headers
    if strings.Count(output, "Transfer-Encoding: chunked") > 1 {
         t.Errorf("Duplicate Transfer-Encoding header detected: %q", output)
    }
    
    w.Release()
}

func TestResponseWriter_WriteString_Chunked(t *testing.T) {
    conn := &MockConnection{}
    reader := bufio.NewReader(conn)
    writer := bufio.NewWriter(conn)
    ctx := appcontext.NewRequestContext(conn, context.Background(), reader, writer)
    req, _ := http.NewRequest("GET", "/", nil)

    w := NewResponseWriter(ctx, req)
    
    // Simulate initial write to trigger headers (and chunked mode)
    // or just rely on EnsureHeaderSent logic inside WriteString
    
    msg := "Hello"
    // WriteString should trigger headers and set chunked because Content-Length is missing
    n, err := w.WriteString(msg)
    if err != nil {
        t.Fatalf("WriteString failed: %v", err)
    }
    if n != len(msg) {
        t.Errorf("Expected %d bytes written, got %d", len(msg), n)
    }
    
    w.Flush()
    
    output := conn.writeBuf.String()
    
    // Check for Transfer-Encoding: chunked header
    if !strings.Contains(output, "Transfer-Encoding: chunked") {
        t.Error("Expected Transfer-Encoding: chunked header")
    }
    
    // Check for chunk format: 5\r\nHello\r\n
    expectedChunk := "5\r\nHello\r\n"
    if !strings.Contains(output, expectedChunk) {
        t.Errorf("Expected chunk %q in output, got %q", expectedChunk, output)
    }
    
    w.Release()
}

func TestResponseWriter_Trailers(t *testing.T) {
	conn := &MockConnection{}
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	ctx := appcontext.NewRequestContext(conn, context.Background(), reader, writer)
	req, _ := http.NewRequest("GET", "/", nil)

	w := NewResponseWriter(ctx, req)

	// Set a Trailer header
	w.Trailer().Set("X-Trailer-Test", "value")

	msg := "Body"
	// Write body to trigger headers. Since Content-Length is missing and trailers are present,
	// it should use chunked encoding with trailers.
	if _, err := w.WriteString(msg); err != nil {
		t.Fatalf("WriteString failed: %v", err)
	}

	// EndResponse should write the trailers
	if err := w.EndResponse(); err != nil {
		t.Fatalf("EndResponse failed: %v", err)
	}

	output := conn.writeBuf.String()

	// 1. Check for Transfer-Encoding: chunked, trailers
	if !strings.Contains(output, "Transfer-Encoding: chunked, trailers") {
		t.Errorf("Expected Transfer-Encoding: chunked, trailers header, got output:\n%s", output)
	}

	// 2. Check for the trailer key and value in the output (after the body)
	if !strings.Contains(output, "X-Trailer-Test: value") {
		t.Errorf("Expected trailer 'X-Trailer-Test: value' in output, got output:\n%s", output)
	}

	// 3. Verify structure: 0\r\n -> Trailers -> \r\n
	// "0\r\n" indicates the end of chunks.
	lastChunkPos := strings.Index(output, "0\r\n")
	if lastChunkPos == -1 {
		t.Errorf("Expected last chunk '0\\r\\n' in output")
	}

	trailerPos := strings.Index(output, "X-Trailer-Test: value")
	if trailerPos == -1 {
		t.Errorf("Expected trailer in output")
	}

	if trailerPos < lastChunkPos {
		t.Errorf("Trailers should appear AFTER the last chunk (0\\r\\n). Got trailer at %d, last chunk at %d", trailerPos, lastChunkPos)
	}

	// Check for final CRLF after trailers (simplistic check)
	if !strings.HasSuffix(output, "\r\n") {
		t.Errorf("Output should end with CRLF")
	}
	
	w.Release()
}

func TestResponseWriter_Hijack(t *testing.T) {
	conn := &MockConnection{}
	// Prepare some data for the hijacked connection to read
	conn.writeBuf.WriteString("Hijacked Data") // Simulate data from client (in writeBuf for mock?)
	// Wait, MockConnection.Read reads from readBuf.
	// MockConnection.Write writes to writeBuf.
	// So we should put data in readBuf for the hijacked conn to read.
	conn.readBuf.WriteString("Client Message")

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	ctx := appcontext.NewRequestContext(conn, context.Background(), reader, writer)
	req, _ := http.NewRequest("GET", "/", nil)

	w := NewResponseWriter(ctx, req)

	// 1. Hijack the connection
	hijacker, ok := interface{}(w).(Hijacker)
	if !ok {
		t.Fatal("ResponseWriter does not implement Hijacker")
	}

	hijackedConn, bufrw, err := hijacker.Hijack()
	if err != nil {
		t.Fatalf("Hijack failed: %v", err)
	}
	defer hijackedConn.Close()

	if !w.Hijacked() {
		t.Error("Hijacked() should return true")
	}

	// 2. Verify Writer is disabled
	if _, err := w.Write([]byte("Should fail")); err != http.ErrHijacked {
		t.Errorf("Expected http.ErrHijacked, got %v", err)
	}

	// 3. Verify interaction with hijacked connection
	// Read from bufio
	line, _, err := bufrw.Reader.ReadLine()
	if err != nil {
		t.Fatalf("Failed to read from hijacked buffer: %v", err)
	}
	if string(line) != "Client Message" {
		t.Errorf("Expected 'Client Message', got '%s'", string(line))
	}

	// Write to bufio
	msg := "Server Reply"
	bufrw.Writer.WriteString(msg)
	bufrw.Writer.Flush()

	if !strings.Contains(conn.writeBuf.String(), msg) {
		t.Errorf("Expected '%s' in output, got '%s'", msg, conn.writeBuf.String())
	}

	w.Release()
}

func TestResponseWriter_ReadFrom(t *testing.T) {
	conn := &MockConnection{}
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	ctx := appcontext.NewRequestContext(conn, context.Background(), reader, writer)
	req, _ := http.NewRequest("GET", "/", nil)

	w := NewResponseWriter(ctx, req)

	// Simulate copying data from a reader (e.g., file)
	srcData := "Data from Reader"
	src := bytes.NewBufferString(srcData)

	// io.Copy uses WriteTo or ReadFrom if available
	n, err := io.Copy(w, src)
	if err != nil {
		t.Fatalf("io.Copy failed: %v", err)
	}
	if n != int64(len(srcData)) {
		t.Errorf("Expected %d bytes written, got %d", len(srcData), n)
	}

	w.Flush()
	output := conn.writeBuf.String()

	// Check headers (Content-Length logic inside ReadFrom/Write triggers implicit chunked if no CL)
	// Since no Content-Length set, it defaults to chunked in current implementation of ensureHeaderSent
	if !strings.Contains(output, "Transfer-Encoding: chunked") {
		t.Errorf("Expected Chunked encoding, got output:\n%s", output)
	}

	// Check body
	if !strings.Contains(output, srcData) {
		t.Errorf("Expected body '%s' in output, got output:\n%s", srcData, output)
	}

	w.Release()
}

func BenchmarkResponseWriter_Write(b *testing.B) {
	// Setup
	conn := &MockConnection{}
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	ctx := appcontext.NewRequestContext(conn, context.Background(), reader, writer)
	req, _ := http.NewRequest("GET", "/", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := NewResponseWriter(ctx, req)
		
		// Simulate standard response
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Hello, World!"))
		w.EndResponse()
		
		w.Release()
	}
}

func BenchmarkStandard_Write(b *testing.B) {
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// httptest.NewRecorder() creates a ResponseRecorder which implements http.ResponseWriter
		// This simulates the overhead of standard library's response handling (conceptually)
		// Note: Real net/http server has more overhead, but this measures the writer part.
		w := httptest.NewRecorder()
		
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Hello, World!"))
		// Standard writer doesn't have EndResponse or Release explicitly in user code,
		// but server does flush. Recorder just writes to buffer.
	}
}