package websocket

import (
	"bufio"
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DevNewbie1826/hon/pkg/adaptor"
	"github.com/gobwas/ws"
)

var _ adaptor.Hijacker = (*MockHijacker)(nil)

// --- Mocks ---

// MockConn implements net.Conn and has IsActive method.
type MockConn struct {
	net.Conn
	active bool
	buf    *bytes.Buffer
}

func NewMockConn() *MockConn {
	return &MockConn{
		active: true,
		buf:    new(bytes.Buffer),
	}
}

func (m *MockConn) Read(p []byte) (n int, err error) {
	return m.buf.Read(p)
}

func (m *MockConn) Write(p []byte) (n int, err error) {
	return len(p), nil
}

func (m *MockConn) Close() error {
	m.active = false
	return nil
}

func (m *MockConn) IsActive() bool { return m.active }

// Additional net.Conn methods to satisfy interface
func (m *MockConn) LocalAddr() net.Addr                { return nil }
func (m *MockConn) RemoteAddr() net.Addr               { return nil }
func (m *MockConn) SetDeadline(t time.Time) error      { return nil }
func (m *MockConn) SetReadDeadline(t time.Time) error  { return nil }
func (m *MockConn) SetWriteDeadline(t time.Time) error { return nil }

// MockHijacker simulates Hon's response writer.
type MockHijacker struct {
	http.ResponseWriter
	Conn *MockConn
	RW   *bufio.ReadWriter
	ReadHandler adaptor.ReadHandler
}

func (m *MockHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return m.Conn, m.RW, nil
}

func (m *MockHijacker) SetReadHandler(h adaptor.ReadHandler) {
	m.ReadHandler = h
}

// MockHandler tracks callbacks.
type MockHandler struct {
	DefaultHandler
	OpenCnt    int32
	MessageCnt int32
	CloseCnt   int32
	LastErr    error
}

func (h *MockHandler) OnOpen(c net.Conn) {
	atomic.AddInt32(&h.OpenCnt, 1)
}

func (h *MockHandler) OnMessage(c net.Conn, op ws.OpCode, p []byte, fin bool) {
	atomic.AddInt32(&h.MessageCnt, 1)
}

func (h *MockHandler) OnClose(c net.Conn, err error) {
	atomic.AddInt32(&h.CloseCnt, 1)
	h.LastErr = err
}

// --- Tests ---

func TestUpgrade_Success(t *testing.T) {
	mc := NewMockConn()
	rw := bufio.NewReadWriter(bufio.NewReader(mc), bufio.NewWriter(mc))
	mh := &MockHijacker{ResponseWriter: httptest.NewRecorder(), Conn: mc, RW: rw}
	
	handler := &MockHandler{}
	
	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Sec-WebSocket-Version", "13")

	err := Upgrade(mh, req, handler)
	if err != nil {
		t.Fatalf("Upgrade failed: %v", err)
	}

	if atomic.LoadInt32(&handler.OpenCnt) != 1 {
		t.Errorf("OnOpen not called")
	}
	if mh.ReadHandler == nil {
		t.Errorf("ReadHandler not set")
	}
}

// TestOnCloseOnce verifies that OnClose is called exactly once when a Close frame is received.
func TestOnCloseOnce(t *testing.T) {
	mc := NewMockConn()
	// Write a Close frame to the buffer
	ws.WriteFrame(mc.buf, ws.NewCloseFrame(ws.NewCloseFrameBody(ws.StatusNormalClosure, "")))

	rw := bufio.NewReadWriter(bufio.NewReader(mc), bufio.NewWriter(mc))
	mh := &MockHijacker{ResponseWriter: httptest.NewRecorder(), Conn: mc, RW: rw}
	handler := &MockHandler{}

	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Key", "key")
	
	Upgrade(mh, req, handler)

	// Simulate the reactor loop calling the handler
	mh.ReadHandler(mc, rw)

	if atomic.LoadInt32(&handler.CloseCnt) != 1 {
		t.Errorf("OnClose called %d times, expected 1", handler.CloseCnt)
	}
}

// TestOnClose_IsActive verifies OnClose is called when connection becomes inactive.
func TestOnClose_IsActive(t *testing.T) {
	mc := NewMockConn()
	mc.active = false // Simulate broken connection

	rw := bufio.NewReadWriter(bufio.NewReader(mc), bufio.NewWriter(mc))
	mh := &MockHijacker{ResponseWriter: httptest.NewRecorder(), Conn: mc, RW: rw}
	handler := &MockHandler{}

	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Key", "key")
	
	Upgrade(mh, req, handler)

	// Simulate loop
	mh.ReadHandler(mc, rw)

	if atomic.LoadInt32(&handler.CloseCnt) != 1 {
		t.Errorf("OnClose called %d times on inactive connection, expected 1", handler.CloseCnt)
	}
}

// TestStreamingLargePayload verifies that large payloads are chunked and don't error.
func TestStreamingLargePayload(t *testing.T) {
	mc := NewMockConn()
	// Create a large frame (e.g. 70KB) which is > default chunk size (64KB)
	// This ensures OnMessage is called multiple times.
	payloadSize := 70 * 1024
	
	// Write Header: Fin=1, Op=Text, Len=70KB (requires extended payload length 127)
	mc.buf.WriteByte(0x81) // Fin | Text
	mc.buf.WriteByte(127)  // Mask=0 | Len=127 (64-bit)
	
	// Write 64-bit length for 70KB
	size := uint64(payloadSize)
	for i := 56; i >= 0; i -= 8 {
		mc.buf.WriteByte(byte(size >> i))
	}
	
	// Write Payload
	data := make([]byte, payloadSize)
	mc.buf.Write(data)

	rw := bufio.NewReadWriter(bufio.NewReader(mc), bufio.NewWriter(mc))
	mh := &MockHijacker{ResponseWriter: httptest.NewRecorder(), Conn: mc, RW: rw}
	handler := &MockHandler{}

	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Key", "key")
	
	Upgrade(mh, req, handler)

	mh.ReadHandler(mc, rw)

	if atomic.LoadInt32(&handler.CloseCnt) != 0 {
		t.Errorf("OnClose called unexpectedly: %v", handler.LastErr)
	}
	// 70KB payload split into 64KB chunks -> ceil(70/64) = 2 chunks (64KB + 6KB)
	expectedChunks := int32(2)
	if atomic.LoadInt32(&handler.MessageCnt) != expectedChunks {
		t.Errorf("OnMessage called %d times, expected %d", handler.MessageCnt, expectedChunks)
	}
}

// --- Benchmarks ---

// BenchmarkReadHeaderZeroAlloc verifies allocation behavior of our custom parser.
// Expectation: 0 Allocs/op.
func BenchmarkReadHeaderZeroAlloc(b *testing.B) {
	// Prepare a buffer with a valid frame header (Ping)
	// Fin=1, Op=Ping(9), Len=0
	// 1000 1001 (0x89) | 0000 0000 (0x00)
	raw := []byte{0x89, 0x00}
	
	// Pre-allocate reader
	r := bytes.NewReader(raw)
	br := bufio.NewReader(r)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		r.Reset(raw)
		br.Reset(r)
		_, err := readHeaderZeroAlloc(br)
		if err != nil {
			b.Fatal(err)
		}
	}
}
