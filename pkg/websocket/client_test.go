package websocket

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// MockServerHandler echoes messages back
type MockServerHandler struct {
	DefaultHandler
}

func (h *MockServerHandler) OnMessage(c net.Conn, op ws.OpCode, payload []byte) {
	// Echo back
	wsutil.WriteServerMessage(c, op, payload)
}

// MockClientHandler receives messages and counts them
type MockClientHandler struct {
	DefaultHandler
	recvCount int
	done      chan struct{}
}

func (h *MockClientHandler) OnMessage(c net.Conn, op ws.OpCode, payload []byte) {
	h.recvCount++
	if h.recvCount >= 1 {
		close(h.done)
	}
}

func startHandshakeServer(t *testing.T, handle func(request string, conn net.Conn)) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = ln.Close()
	})

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		br := bufio.NewReader(conn)
		var request strings.Builder
		for {
			line, _ := br.ReadString('\n')
			request.WriteString(line)
			if line == "\r\n" {
				break
			}
		}

		handle(request.String(), conn)
	}()

	return ln.Addr().String()
}

func headerValue(request, key string) string {
	prefix := key + ":"
	for _, line := range strings.Split(request, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), strings.ToLower(prefix)) {
			return strings.TrimSpace(line[len(prefix):])
		}
	}
	return ""
}

func validHandshakeResponse(request string) string {
	return "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + computeAcceptKey(headerValue(request, "Sec-WebSocket-Key")) + "\r\n\r\n"
}

func TestClientDial(t *testing.T) {
	addr := startHandshakeServer(t, func(request string, conn net.Conn) {
		_, _ = conn.Write([]byte(validHandshakeResponse(request)))
		_ = wsutil.WriteServerMessage(conn, ws.OpText, []byte("Hello Client"))
		for {
			_, _, err := wsutil.ReadClientData(conn)
			if err != nil {
				return
			}
		}
	})

	done := make(chan struct{})
	handler := &MockClientHandler{done: done}

	url := fmt.Sprintf("ws://%s/ws_test", addr)

	err := Dial(url, handler)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for message")
	}
}

func TestClientDial_RejectsInvalidAccept(t *testing.T) {
	addr := startHandshakeServer(t, func(request string, conn net.Conn) {
		_, _ = conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: invalid\r\n\r\n"))
	})

	err := Dial(fmt.Sprintf("ws://%s/ws_test", addr), &MockClientHandler{done: make(chan struct{})})
	if err == nil {
		t.Fatal("expected invalid accept key to fail the handshake")
	}
}

func TestClientDial_RejectsMissingUpgradeHeader(t *testing.T) {
	addr := startHandshakeServer(t, func(request string, conn net.Conn) {
		_, _ = conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\n" +
			"Connection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: " + computeAcceptKey(headerValue(request, "Sec-WebSocket-Key")) + "\r\n\r\n"))
	})

	err := Dial(fmt.Sprintf("ws://%s/ws_test", addr), &MockClientHandler{done: make(chan struct{})})
	if err == nil {
		t.Fatal("expected missing upgrade header to fail the handshake")
	}
}

func TestClientDial_PropagatesHandshakeHeaderTooLarge(t *testing.T) {
	addr := startHandshakeServer(t, func(request string, conn net.Conn) {
		_, _ = conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\n" + strings.Repeat("X-Test: aaaaaaaaaaaaaaaaaaaaaaaa\r\n", 200)))
	})

	client := NewClient()
	client.DialTimeout = 100 * time.Millisecond

	err := client.Dial(fmt.Sprintf("ws://%s/ws_test", addr), &MockClientHandler{done: make(chan struct{})})
	if err == nil {
		t.Fatal("expected oversized header to fail the handshake")
	}
	if !strings.Contains(err.Error(), "response header too large") {
		t.Fatalf("expected oversized header error, got %v", err)
	}
}

func TestClientDial_AllowsDuplicateConnectionHeaders(t *testing.T) {
	addr := startHandshakeServer(t, func(request string, conn net.Conn) {
		_, _ = conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\n" +
			"Connection: Upgrade\r\n" +
			"Connection: keep-alive\r\n" +
			"Upgrade: websocket\r\n" +
			"Sec-WebSocket-Accept: " + computeAcceptKey(headerValue(request, "Sec-WebSocket-Key")) + "\r\n\r\n"))
		_ = wsutil.WriteServerMessage(conn, ws.OpText, []byte("Hello Client"))
		for {
			_, _, err := wsutil.ReadClientData(conn)
			if err != nil {
				return
			}
		}
	})

	done := make(chan struct{})
	err := Dial(fmt.Sprintf("ws://%s/ws_test", addr), &MockClientHandler{done: done})
	if err != nil {
		t.Fatalf("expected duplicate Connection headers to be accepted, got %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for client message after handshake")
	}
}
