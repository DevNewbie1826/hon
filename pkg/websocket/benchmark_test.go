package websocket

import (
	"bytes"
	"testing"

	"github.com/cloudwego/netpoll"
	"github.com/gobwas/ws"
)

// MockConnection for Netpoll using LinkBuffer
type MockNetpollConn struct {
	netpoll.Connection
	r netpoll.Reader
	w *bytes.Buffer
}

func (m *MockNetpollConn) Reader() netpoll.Reader { return m.r }
func (m *MockNetpollConn) Writer() netpoll.Writer { return netpoll.NewWriter(m.w) }
func (m *MockNetpollConn) IsActive() bool         { return true }
func (m *MockNetpollConn) Close() error           { return nil }

// BenchmarkServeReactor measures allocations in the hot path of the client
func BenchmarkServeReactor(b *testing.B) {
	// Prepare a frame
	payload := []byte("Hello Benchmark")
	frame := bytes.NewBuffer(nil)

	ws.WriteHeader(frame, ws.Header{
		Fin:    true,
		OpCode: ws.OpText,
		Length: int64(len(payload)),
	})
	frame.Write(payload)
	frameBytes := frame.Bytes()

	handler := &DefaultHandler{}
	cfg := &Config{}

	// Use LinkBuffer which implements netpoll.Reader fully
	lb := netpoll.NewLinkBuffer()

	mockConn := &MockNetpollConn{
		r: lb,
		w: bytes.NewBuffer(nil),
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Fill buffer
		lb.WriteBinary(frameBytes)
		// No Flush needed for LinkBuffer write to be readable?
		// LinkBuffer.Flush() flushes to underlying writer, but here we write directly to it.
		// LinkBuffer implements Read/Write so writing to it makes data readable.
		lb.Flush()

		// Run Reactor Logic
		_ = ServeReactor(mockConn, handler, cfg)

		// Reset buffer? ServeReactor consumes it.
	}
}

// BenchmarkReadHeaderNetpoll measures header parsing allocations
func BenchmarkReadHeaderNetpoll(b *testing.B) {
	frame := bytes.NewBuffer(nil)
	ws.WriteHeader(frame, ws.Header{
		Fin:    true,
		OpCode: ws.OpText,
		Length: 125,
	})
	headerBytes := frame.Bytes()

	lb := netpoll.NewLinkBuffer()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		lb.WriteBinary(headerBytes)
		lb.Flush()

		_, _, _ = readHeaderNetpoll(lb)

		// Should we release or reset?
		// ServeReactor/readHeader consumes data (Skip/Next).
		// LinkBuffer automatically manages memory.
	}
}
