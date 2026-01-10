package websocket

import (
	"net"

	"github.com/gobwas/ws"
)

// Handler defines the event callbacks for WebSocket connections.
// It is designed to be stateless to fit within Hon's reactor pattern.
type Handler interface {
	// OnOpen is called when the WebSocket handshake is successful.
	OnOpen(c net.Conn)

	// OnMessage is called when a message chunk is received.
	// For large messages, this may be called multiple times.
	// 'fin' indicates if this is the final chunk of the logical message.
	OnMessage(c net.Conn, op ws.OpCode, payload []byte, fin bool)

	// OnClose is called when the connection is closed.
	// err might be nil if it's a normal closure.
	OnClose(c net.Conn, err error)

	// OnPing is called when a Ping frame is received.
	// Default implementation should send a Pong.
	OnPing(c net.Conn, payload []byte)

	// OnPong is called when a Pong frame is received.
	OnPong(c net.Conn, payload []byte)
}

// DefaultHandler provides empty implementations for optional methods.
type DefaultHandler struct{}

func (d *DefaultHandler) OnOpen(c net.Conn)                                      {}
func (d *DefaultHandler) OnMessage(c net.Conn, op ws.OpCode, p []byte, fin bool) {}
func (d *DefaultHandler) OnClose(c net.Conn, err error)                          {}
func (d *DefaultHandler) OnPing(c net.Conn, p []byte)                            {}
func (d *DefaultHandler) OnPong(c net.Conn, p []byte)                            {}