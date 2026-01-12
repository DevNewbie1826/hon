package websocket

import (
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/DevNewbie1826/hon/pkg/engine"
	"github.com/DevNewbie1826/hon/pkg/server"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func TestWebSocketIntegration(t *testing.T) {
	// 1. Start Server
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		_ = Upgrade(w, r, &DefaultHandler{
			OnMessageFunc: func(c net.Conn, op ws.OpCode, p []byte, fin bool) {
				// Echo message back
				_ = wsutil.WriteServerMessage(c, op, p)
			},
		})
	})

	eng := engine.NewEngine(mux)
	srv := server.NewServer(eng)
	addr := "127.0.0.1:18265"
	
	go srv.Serve(addr)
	time.Sleep(100 * time.Millisecond)

	// 2. Client Setup
	received := make(chan string, 1)
	var clientConn net.Conn

	err := Dial("ws://"+addr+"/ws", &DefaultHandler{
		OnOpenFunc: func(c net.Conn) {
			clientConn = c
		},
		OnMessageFunc: func(c net.Conn, op ws.OpCode, p []byte, fin bool) {
			received <- string(p)
		},
	})
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}

	// Wait for reactor to call OnOpen
	time.Sleep(50 * time.Millisecond)
	if clientConn == nil {
		t.Fatal("Client connection not established")
	}

	// 3. Test Bidirectional Communication
	messages := []string{"Hello", "Hon", "Reactor", "World"}
	for _, m := range messages {
		_ = wsutil.WriteClientMessage(clientConn, ws.OpText, []byte(m))
		
		select {
		case r := <-received:
			if r != m {
				t.Errorf("Expected %q, got %q", m, r)
			}
		case <-time.After(1 * time.Second):
			t.Fatalf("Timeout waiting for message: %s", m)
		}
	}

	// 4. Test Closing
	_ = clientConn.Close()
	time.Sleep(50 * time.Millisecond)
}
