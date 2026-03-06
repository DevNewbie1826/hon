package websocket

import (
	"bytes"
	"fmt"
	"net"
	"testing"
	"time"

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

type benchmarkNetConn struct {
	bytes.Buffer
}

func (c *benchmarkNetConn) Close() error                     { return nil }
func (c *benchmarkNetConn) LocalAddr() net.Addr              { return nil }
func (c *benchmarkNetConn) RemoteAddr() net.Addr             { return nil }
func (c *benchmarkNetConn) SetDeadline(time.Time) error      { return nil }
func (c *benchmarkNetConn) SetReadDeadline(time.Time) error  { return nil }
func (c *benchmarkNetConn) SetWriteDeadline(time.Time) error { return nil }

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

func BenchmarkValidateHandshakeResponse(b *testing.B) {
	secKey := "dGhlIHNhbXBsZSBub25jZQ=="
	headerBlock := []byte(
		"Upgrade: websocket\r\n" +
			"Connection: keep-alive, Upgrade\r\n" +
			fmt.Sprintf("Sec-WebSocket-Accept: %s\r\n", computeAcceptKey(secKey)),
	)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if err := validateHandshakeResponse(headerBlock, secKey); err != nil {
			b.Fatalf("validateHandshakeResponse failed: %v", err)
		}
	}
}

func BenchmarkWriteMessage_TextUncompressed(b *testing.B) {
	payload := []byte("Hello Benchmark")
	cfg := &Config{}
	conn := &benchmarkNetConn{}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		conn.Reset()
		if err := WriteMessage(conn, ws.OpText, payload, cfg); err != nil {
			b.Fatalf("WriteMessage failed: %v", err)
		}
	}
}

func BenchmarkWriteMessage_ClientMaskedText(b *testing.B) {
	payload := []byte("Hello Benchmark")
	cfg := &Config{}
	conn := &benchmarkNetConn{}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		conn.Reset()
		if err := WriteClientMessage(conn, ws.OpText, payload, cfg); err != nil {
			b.Fatalf("WriteClientMessage failed: %v", err)
		}
	}
}
