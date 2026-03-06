package server

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
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

func TestServer_ShutdownDuringStartup(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := NewServer(engine.NewEngine(mux))

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(":19998")
	}()

	select {
	case <-srv.started:
	case <-time.After(time.Second):
		t.Fatal("Serve did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
		defer cleanupCancel()
		_ = srv.Shutdown(cleanupCtx)
		t.Fatal("server should stop even if Shutdown is called during startup")
	}
}

func TestServer_MaxConns_DoesNotUnderflowCounter(t *testing.T) {
	mux := http.NewServeMux()
	blocked := make(chan struct{})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		<-blocked
		w.WriteHeader(http.StatusOK)
	})

	srv := NewServer(
		engine.NewEngine(mux),
		WithMaxConns(1),
		WithReadTimeout(time.Second),
		WithWriteTimeout(time.Second),
		WithKeepAliveTimeout(time.Second),
	)

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(":19997")
	}()

	var conn1 net.Conn
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", "127.0.0.1:19997", 50*time.Millisecond)
		if err == nil {
			conn1 = c
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conn1 == nil {
		t.Fatal("failed to establish first connection")
	}
	defer conn1.Close()

	if _, err := conn1.Write([]byte("GET / HTTP/1.1\r\nHost: localhost\r\n\r\n")); err != nil {
		t.Fatalf("first request write failed: %v", err)
	}

	conn2, err := net.DialTimeout("tcp", "127.0.0.1:19997", time.Second)
	if err != nil {
		t.Fatalf("second dial failed: %v", err)
	}
	defer conn2.Close()

	time.Sleep(100 * time.Millisecond)

	if got := atomic.LoadInt32(&srv.connsCount); got != 1 {
		close(blocked)
		t.Fatalf("expected active connection count to remain 1 after overload rejection, got %d", got)
	}

	close(blocked)
	buf := make([]byte, 256)
	_, _ = conn1.Read(buf)
	if err := conn1.Close(); err != nil {
		t.Fatalf("failed to close first connection: %v", err)
	}

	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&srv.connsCount) == 0 {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = srv.Shutdown(ctx)
			<-done
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	<-done
	t.Fatalf("expected connection count to return to 0, got %d", atomic.LoadInt32(&srv.connsCount))
}

func TestServer_ShutdownBeforeServe_ReturnsNil(t *testing.T) {
	srv := NewServer(engine.NewEngine(http.NewServeMux()))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("expected Shutdown before Serve to return nil, got %v", err)
	}
}

func TestServer_ShutdownBeforeServe_PreventsLaterServe(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := NewServer(engine.NewEngine(mux))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("expected Shutdown before Serve to return nil, got %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(":19996")
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned error after prior Shutdown: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Serve should exit immediately after a prior Shutdown request")
	}

	conn, err := net.DialTimeout("tcp", "127.0.0.1:19996", 50*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatal("server should not start after Shutdown was already requested")
	}
}
