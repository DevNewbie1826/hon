package websocket

import (
	"net"

	"github.com/gobwas/ws"
)

// Handler defines the event callbacks for WebSocket connections.
// It is designed to be stateless to fit within Hon's reactor pattern.
type Handler interface {
	OnOpen(c net.Conn)
	// OnMessage is called when a complete message is received.
	// Hon v0.6.0+ guarantees that payload is the full message (reassembled and decompressed).
	OnMessage(c net.Conn, op ws.OpCode, payload []byte)

	// OnClose is called when the connection is closed.
	OnClose(c net.Conn, err error)

	// OnPing is called when a ping frame is received.
	OnPing(c net.Conn, payload []byte)

	// OnPong is called when a pong frame is received.
	OnPong(c net.Conn, payload []byte)
}

// DefaultHandler provides optional implementations via function fields.
// This allows for quick setup without creating a new struct type.
type DefaultHandler struct {
	OnOpenFunc    func(c net.Conn)
	OnMessageFunc func(c net.Conn, op ws.OpCode, payload []byte)
	OnCloseFunc   func(c net.Conn, err error)
	OnPingFunc    func(c net.Conn, payload []byte)
	OnPongFunc    func(c net.Conn, payload []byte)
}

func (h *DefaultHandler) OnOpen(c net.Conn) {
	if h.OnOpenFunc != nil {
		h.OnOpenFunc(c)
	}
}

func (h *DefaultHandler) OnMessage(c net.Conn, op ws.OpCode, payload []byte) {
	if h.OnMessageFunc != nil {
		h.OnMessageFunc(c, op, payload)
	}
}

func (d *DefaultHandler) OnClose(c net.Conn, err error) {
	if d.OnCloseFunc != nil {
		d.OnCloseFunc(c, err)
	}
}

func (d *DefaultHandler) OnPing(c net.Conn, p []byte) {
	if d.OnPingFunc != nil {
		d.OnPingFunc(c, p)
	}
}

func (d *DefaultHandler) OnPong(c net.Conn, p []byte) {
	if d.OnPongFunc != nil {
		d.OnPongFunc(c, p)
	}
}
