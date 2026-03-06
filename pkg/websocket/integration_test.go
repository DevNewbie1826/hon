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

func reserveLoopbackAddr(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func waitForTCPServer(t *testing.T, addr string) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become reachable", addr)
}

func waitForConn(t *testing.T, ch <-chan net.Conn) net.Conn {
	t.Helper()

	select {
	case conn := <-ch:
		return conn
	case <-time.After(time.Second):
		t.Fatal("client connection not established")
		return nil
	}
}

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

	addr := reserveLoopbackAddr(t)

	go srv.Serve(addr)
	waitForTCPServer(t, addr)

	// 2. Client Setup
	received := make(chan string, 1)
	opened := make(chan net.Conn, 1)

	err := Dial("ws://"+addr+"/ws", &DefaultHandler{
		OnOpenFunc: func(c net.Conn) {
			opened <- c
		},
		OnMessageFunc: func(c net.Conn, op ws.OpCode, p []byte) {
			received <- string(p)
		},
	})
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}

	clientConn := waitForConn(t, opened)

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
	addr := reserveLoopbackAddr(t)

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
	waitForTCPServer(t, addr)
	defer srv.Shutdown(context.Background())

	// Client with custom header and cookie
	err := Dial("ws://"+addr+"/ws", &DefaultHandler{},
		WithHeader("X-Custom-Auth", "secret-token"),
		WithCookie(&http.Cookie{Name: "session_id", Value: "12345"}),
	)
	if err != nil {
		t.Fatalf("Dial with headers/cookies failed: %v", err)
	}
}

func TestWebSocket_Compression(t *testing.T) {
	// Random Port
	addr := reserveLoopbackAddr(t)

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
	waitForTCPServer(t, addr)
	defer srv.Shutdown(context.Background())

	// 2. Client with Compression Enabled
	received := make(chan string, 1)
	opened := make(chan net.Conn, 1)

	cfg := &Config{EnableCompression: true}

	err := Dial("ws://"+addr+"/ws", &DefaultHandler{
		OnOpenFunc: func(c net.Conn) {
			opened <- c
		},
		OnMessageFunc: func(c net.Conn, op ws.OpCode, p []byte) {
			received <- string(p)
		},
	}, WithEnableCompression(true))

	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}

	clientConn := waitForConn(t, opened)

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
