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
	w netpoll.Writer
}

func (m *MockNetpollConn) Reader() netpoll.Reader { return m.r }
func (m *MockNetpollConn) Writer() netpoll.Writer { return m.w }
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

	// Use LinkBuffer which implements Reader and Writer fully
	lb := netpoll.NewLinkBuffer()

	mockConn := &MockNetpollConn{
		r: lb,
		w: lb,
	}

	b.ResetTimer()
	b.ReportAllocs()

	assembler := NewAssembler(cfg)
	for i := 0; i < b.N; i++ {
		// Fill buffer
		lb.WriteBinary(frameBytes)
		lb.Flush()

		// Run Reactor Logic (Unified ServeConn)
		_ = ServeConn(mockConn, nil, handler, cfg, assembler)
	}
}

// BenchmarkParseHeader measures header parsing allocations (Replacement for ReadHeaderNetpoll)
func BenchmarkParseHeader(b *testing.B) {
	frame := bytes.NewBuffer(nil)
	ws.WriteHeader(frame, ws.Header{
		Fin:    true,
		OpCode: ws.OpText,
		Length: 125,
	})
	headerBytes := frame.Bytes()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _, _ = parseHeader(headerBytes)
	}
}
