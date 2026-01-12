package websocket

import (
	"context"
	"net"
	"net/http"
	"strings"
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
			OnMessageFunc: func(c net.Conn, op ws.OpCode, p []byte) {
				// Echo message back
				_ = wsutil.WriteServerMessage(c, op, p)
			},
		})
	})

	eng := engine.NewEngine(mux)
	srv := server.NewServer(eng)

	// Use a fresh port
	addr := "127.0.0.1:18267"

	go srv.Serve(addr)
	time.Sleep(200 * time.Millisecond)

	// 2. Client Setup
	received := make(chan string, 1)
	var clientConn net.Conn

	err := Dial("ws://"+addr+"/ws", &DefaultHandler{
		OnOpenFunc: func(c net.Conn) {
			clientConn = c
		},
		OnMessageFunc: func(c net.Conn, op ws.OpCode, p []byte) {
			received <- string(p)
		},
	})
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}

	// Wait for reactor to call OnOpen
	time.Sleep(200 * time.Millisecond)
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

func TestWebSocket_CustomHeadersAndCookies(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // Release port so Serve can bind to it

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		// Verify Header
		if r.Header.Get("X-Custom-Auth") != "secret-token" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		// Verify Cookie
		cookie, err := r.Cookie("session_id")
		if err != nil || cookie.Value != "12345" {
			http.Error(w, "Invalid Cookie", http.StatusForbidden)
			return
		}

		_ = Upgrade(w, r, &DefaultHandler{
			OnOpenFunc: func(c net.Conn) {
				// Handshake success
			},
		})
	})

	srv := server.NewServer(engine.NewEngine(mux))
	go srv.Serve(addr) // Use the random port
	time.Sleep(200 * time.Millisecond)
	defer srv.Shutdown(context.Background())

	// Client with custom header and cookie
	err = Dial("ws://"+addr+"/ws", &DefaultHandler{},
		WithHeader("X-Custom-Auth", "secret-token"),
		WithCookie(&http.Cookie{Name: "session_id", Value: "12345"}),
	)
	if err != nil {
		t.Fatalf("Dial with headers/cookies failed: %v", err)
	}
}

func TestWebSocket_Compression(t *testing.T) {
	// Random Port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	// 1. Server with Compression Enabled
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		cfg := &Config{
			EnableCompression: true,
		}
		_ = Upgrade(w, r, &DefaultHandler{
			OnMessageFunc: func(c net.Conn, op ws.OpCode, p []byte) {
				// Echo back with compression if applicable
				_ = WriteMessage(c, op, p, cfg)
			},
		}, WithEnableCompression(true))
	})

	srv := server.NewServer(engine.NewEngine(mux))
	go srv.Serve(addr)
	time.Sleep(200 * time.Millisecond)
	defer srv.Shutdown(context.Background())

	// 2. Client with Compression Enabled
	received := make(chan string, 1)
	var clientConn net.Conn

	cfg := &Config{EnableCompression: true}

	err = Dial("ws://"+addr+"/ws", &DefaultHandler{
		OnOpenFunc: func(c net.Conn) {
			clientConn = c
		},
		OnMessageFunc: func(c net.Conn, op ws.OpCode, p []byte) {
			received <- string(p)
		},
	}, WithEnableCompression(true))

	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	if clientConn == nil {
		t.Fatal("Client connection failed")
	}

	// 3. Send Large Message (> 1KB)
	largeMsg := strings.Repeat("A", 2048) // 2KB
	// Use WriteClientMessage for masking and compression
	err = WriteClientMessage(clientConn, ws.OpText, []byte(largeMsg), cfg)
	if err != nil {
		t.Fatal(err)
	}

	select {
	case r := <-received:
		if len(r) != len(largeMsg) {
			t.Errorf("Length mismatch: got %d, want %d", len(r), len(largeMsg))
		}
		if r != largeMsg {
			t.Errorf("Content mismatch")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Timeout")
	}
}
