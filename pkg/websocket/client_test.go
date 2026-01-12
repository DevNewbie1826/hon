package websocket

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// MockServerHandler echoes messages back
type MockServerHandler struct {
	DefaultHandler
}

func (h *MockServerHandler) OnMessage(c net.Conn, op ws.OpCode, payload []byte, fin bool) {
	// Echo back
	wsutil.WriteServerMessage(c, op, payload)
}

// MockClientHandler receives messages and counts them
type MockClientHandler struct {
	DefaultHandler
	recvCount int
	done      chan struct{}
}

func (h *MockClientHandler) OnMessage(c net.Conn, op ws.OpCode, payload []byte, fin bool) {
	h.recvCount++
	if h.recvCount >= 1 {
		close(h.done)
	}
}

func TestClientDial(t *testing.T) {
	// 1. Start Server
	http.HandleFunc("/ws_test", func(w http.ResponseWriter, r *http.Request) {
		err := Upgrade(w, r, &MockServerHandler{})
		if err != nil {
			// t.Logf("Upgrade failed: %v", err) // Standard http server might fail hijack type assertion if not using Hon Engine, but we can test logic partially?
			// Wait, Upgrade requires adaptor.Hijacker.
			// Standard net/http ResponseWriter doesn't implement it cleanly without the adaptor/engine.
			// However, we can mock the Hijacker interface or use a real listener for the test.
			// For this unit test, let's use a standard net listener and simulate the server side manually
			// OR use the actual Hon stack if possible.
			// Given dependencies, let's try to mock the server behavior using a raw TCP listener to avoid full stack complexity in unit test.
		}
	})

	// Real Server approach using net.Listen to avoid circular dependencies with pkg/server if possible,
	// but Upgrade relies on specific interfaces.
	// Let's create a simple TCP Echo Server that mimics a WS Server for the Client to connect to.

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		br := bufio.NewReader(conn)
		// 1. Read HTTP Upgrade Request
		for {
			line, _ := br.ReadString('\n')
			if line == "\r\n" {
				break
			}
		}

		// 2. Send HTTP Upgrade Response
		conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n\r\n"))

		// 3. Send a WS Message (Hello)
		wsutil.WriteServerMessage(conn, ws.OpText, []byte("Hello Client"))

		// 4. Read Loop (Keep alive)
		for {
			_, _, err := wsutil.ReadClientData(conn)
			if err != nil {
				return
			}
		}
	}()

	// 2. Run Client
	done := make(chan struct{})
	handler := &MockClientHandler{done: done}

	url := fmt.Sprintf("ws://%s/ws_test", addr)

	// Wait a bit for server to be ready
	time.Sleep(100 * time.Millisecond)

	err = Dial(url, handler)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}

	// 3. Wait for message reception
	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for message")
	}
}
