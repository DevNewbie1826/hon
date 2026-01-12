package websocket

import (
	"net"

	"github.com/gobwas/ws"
)

// Handler defines the event callbacks for WebSocket connections.
// It is designed to be stateless to fit within Hon's reactor pattern.
type Handler interface {
	OnOpen(c net.Conn)
	OnMessage(c net.Conn, op ws.OpCode, payload []byte, fin bool)
	OnClose(c net.Conn, err error)
	OnPing(c net.Conn, payload []byte)
	OnPong(c net.Conn, payload []byte)
}

// DefaultHandler provides optional implementations via function fields.
// This allows for quick setup without creating a new struct type.
type DefaultHandler struct {
	OnOpenFunc    func(c net.Conn)
	OnMessageFunc func(c net.Conn, op ws.OpCode, p []byte, fin bool)
	OnCloseFunc   func(c net.Conn, err error)
	OnPingFunc    func(c net.Conn, p []byte)
	OnPongFunc    func(c net.Conn, p []byte)
}

func (d *DefaultHandler) OnOpen(c net.Conn) {
	if d.OnOpenFunc != nil {
		d.OnOpenFunc(c)
	}
}

func (d *DefaultHandler) OnMessage(c net.Conn, op ws.OpCode, p []byte, fin bool) {
	if d.OnMessageFunc != nil {
		d.OnMessageFunc(c, op, p, fin)
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
