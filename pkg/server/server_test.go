package server

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/DevNewbie1826/hon/pkg/engine"
)

func TestServer_ServeAndShutdown(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	eng := engine.NewEngine(mux)

	// Use Options
	srv := NewServer(eng,
		WithReadTimeout(1*time.Second),
		WithWriteTimeout(1*time.Second),
		WithKeepAliveTimeout(1*time.Second),
	)

	// Start server in goroutine
	go func() {
		if err := srv.Serve(":19999"); err != nil {
			// Serve might return error on shutdown, which is expected
		}
	}()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	// Make a request
	conn, err := net.Dial("tcp", "127.0.0.1:19999")
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	conn.Write([]byte("GET / HTTP/1.1\r\nHost: localhost\r\n\r\n"))

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	response := string(buf[:n])
	if response[:15] != "HTTP/1.1 200 OK" {
		t.Errorf("Unexpected response: %s", response)
	}
	conn.Close()

	// Shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown failed: %v", err)
	}
}
