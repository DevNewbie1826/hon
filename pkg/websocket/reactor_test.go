package websocket

import (
	"bytes"
	"io"
	"net"
	"testing"

	"github.com/cloudwego/netpoll"
	"github.com/gobwas/ws"
)

// MockConnection simulates a netpoll connection for testing ServeConn.
type MockConnection struct {
	netpoll.Connection
	reader       *mockNetpollReader
	context      interface{}
	isActiveFlag bool
}

func (m *MockConnection) Reader() netpoll.Reader {
	return m.reader
}

func (m *MockConnection) IsActive() bool {
	return m.isActiveFlag
}

func (m *MockConnection) Close() error {
	m.isActiveFlag = false
	return nil
}

func (m *MockConnection) SetContext(ctx interface{}) error {
	m.context = ctx
	return nil
}

func (m *MockConnection) Context() interface{} {
	return m.context
}

// mockNetpollReader simulates netpoll.Reader behavior.
type mockNetpollReader struct {
	netpoll.Reader
	buffer bytes.Buffer
}

func (m *mockNetpollReader) Len() int {
	return m.buffer.Len()
}

func (m *mockNetpollReader) Peek(n int) ([]byte, error) {
	if m.buffer.Len() < n {
		return m.buffer.Bytes(), nil
	}
	return m.buffer.Bytes()[:n], nil
}

func (m *mockNetpollReader) Skip(n int) error {
	m.buffer.Next(n)
	return nil
}

func (m *mockNetpollReader) Next(n int) ([]byte, error) {
	b := m.buffer.Next(n)
	if len(b) < n {
		return nil, io.EOF
	}
	return b, nil
}

func (m *mockNetpollReader) Release() error {
	return nil
}

// Implement missing methods just to satisfy interface
func (m *mockNetpollReader) ReadBinary(n int) ([]byte, error) { return m.Next(n) }
func (m *mockNetpollReader) ReadString(n int) (string, error) {
	b, err := m.Next(n)
	return string(b), err
}
func (m *mockNetpollReader) ReadByte() (byte, error) {
	b, err := m.Next(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

type LocalMockHandler struct {
	onMessage func(c net.Conn, op ws.OpCode, payload []byte)
}

func (h *LocalMockHandler) OnOpen(c net.Conn)             {}
func (h *LocalMockHandler) OnClose(c net.Conn, err error) {}
func (h *LocalMockHandler) OnMessage(c net.Conn, op ws.OpCode, payload []byte) {
	if h.onMessage != nil {
		h.onMessage(c, op, payload)
	}
}
func (h *LocalMockHandler) OnPing(c net.Conn, payload []byte) {}
func (h *LocalMockHandler) OnPong(c net.Conn, payload []byte) {}

// TestServeReactor_StateLoss demonstrates that unified ServeConn maintains state across frames.
func TestServeReactor_StateLoss(t *testing.T) {
	cfg := &Config{
		MaxFrameSize:      1024 * 1024,
		EnableCompression: true,
	}

	// 1. Create a large message to compress
	originalPayload := bytes.Repeat([]byte("Hello World "), 100) // ~1200 bytes
	compressedPayload, err := CompressData(originalPayload)
	if err != nil {
		t.Fatalf("Failed to compress: %v", err)
	}

	// 2. Prepare WebSocket Frame
	var frameBuf bytes.Buffer
	header := ws.Header{
		Fin:    true,
		Rsv:    4, // RSV1 for compression
		OpCode: ws.OpText,
		Length: int64(len(compressedPayload)),
	}
	if err := ws.WriteHeader(&frameBuf, header); err != nil {
		t.Fatalf("Failed to write header: %v", err)
	}
	frameBuf.Write(compressedPayload)

	// 3. Setup Mock Connection
	mockReader := &mockNetpollReader{}
	conn := &MockConnection{
		reader:       mockReader,
		isActiveFlag: true,
	}

	// Capture received message
	var receivedPayload []byte
	handler := &LocalMockHandler{
		onMessage: func(c net.Conn, op ws.OpCode, payload []byte) {
			receivedPayload = append(receivedPayload, payload...)
		},
	}

	// 4. Send Fragmented Frames (OpContinuation) to verify state persistence
	part1 := originalPayload[:len(originalPayload)/2]
	part2 := originalPayload[len(originalPayload)/2:]

	assembler := NewAssembler(cfg)

	// -- Frame 1 --
	var buf1 bytes.Buffer
	h1 := ws.Header{
		Fin:    false,
		Rsv:    0,
		OpCode: ws.OpText,
		Length: int64(len(part1)),
	}
	ws.WriteHeader(&buf1, h1)
	buf1.Write(part1)

	mockReader.buffer.Write(buf1.Bytes())
	if err := ServeConn(conn, nil, handler, cfg, assembler); err != nil {
		t.Fatalf("Frame 1 error: %v", err)
	}

	if len(receivedPayload) > 0 {
		t.Fatalf("Received message too early! Should wait for Fin")
	}

	// -- Frame 2 --
	var buf2 bytes.Buffer
	h2 := ws.Header{
		Fin:    true,
		Rsv:    0,
		OpCode: ws.OpContinuation,
		Length: int64(len(part2)),
	}
	ws.WriteHeader(&buf2, h2)
	buf2.Write(part2)

	mockReader.buffer.Write(buf2.Bytes())
	if err := ServeConn(conn, nil, handler, cfg, assembler); err != nil {
		t.Fatalf("Frame 2 error: %v", err)
	}

	// 5. Verification
	if receivedPayload == nil {
		t.Fatal("❌ FAILED: No message received! (State lost)")
	}
	if !bytes.Equal(receivedPayload, originalPayload) {
		t.Fatalf("❌ FAILED: Payload mismatch.\nExpected: %s\nGot: %s", string(originalPayload[:20]), string(receivedPayload[:20]))
	}

	t.Log("✅ SUCCESS: State was maintained across frames")
}
