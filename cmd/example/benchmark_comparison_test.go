package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	hengine "github.com/DevNewbie1826/hon/pkg/engine"
	hserver "github.com/DevNewbie1826/hon/pkg/server"
	honws "github.com/DevNewbie1826/hon/pkg/websocket"
	"github.com/gobwas/ws"
	"github.com/gorilla/websocket"
)

// --- Benchmarks ---

func getBenchmarkClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        2000,
			MaxIdleConnsPerHost: 2000,
			IdleConnTimeout:     30 * time.Second,
			DisableKeepAlives:   false,
		},
	}
}

// Handlers

func benchmarkRootHandler(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("Hello Benchmark"))
}

func benchmarkStdWSHandler(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	for {
		mt, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		// Echo back
		if err := conn.WriteMessage(mt, msg); err != nil {
			return
		}
	}
}

type BenchHonWSHandler struct {
	honws.DefaultHandler
}

func (h *BenchHonWSHandler) OnMessage(c net.Conn, op ws.OpCode, payload []byte) {
	// Echo back
	honws.WriteMessage(c, op, payload, nil)
}

func benchmarkHonWSHandler(w http.ResponseWriter, r *http.Request) {
	honws.Upgrade(w, r, &BenchHonWSHandler{})
}

// HTTP Benchmarks

func BenchmarkServer_Standard_HTTP(b *testing.B) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", benchmarkRootHandler)

	// Create Listener explicitly to safely get a free port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)

	client := getBenchmarkClient()
	url := "http://" + addr + "/"

	// Wait for server? (ln is already open)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		resp, err := client.Get(url)
		if err != nil {
			b.Fatal(err)
		}
		// CRITICAL: Read body to EOF to allow connection reuse (Keep-Alive)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

func BenchmarkServer_Hon_HTTP(b *testing.B) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", benchmarkRootHandler)

	eng := hengine.NewEngine(mux)
	srv := hserver.NewServer(eng)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	go func() {
		// Serve will bind again
		srv.Serve(addr)
	}()
	defer srv.Shutdown(context.Background())

	time.Sleep(200 * time.Millisecond) // Give Hon time to re-bind

	client := getBenchmarkClient()
	url := "http://" + addr + "/"

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		resp, err := client.Get(url)
		if err != nil {
			b.Fatalf("Request failed: %v", err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

// WebSocket Benchmarks

func BenchmarkServer_Standard_WS(b *testing.B) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", benchmarkStdWSHandler)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)

	url := "ws://" + addr + "/ws"

	// Single Connection Throughput Test
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		b.Fatalf("Dial failed: %v", err)
	}
	defer c.Close()

	msg := []byte("Hello Benchmark")

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if err := c.WriteMessage(websocket.TextMessage, msg); err != nil {
			b.Fatal(err)
		}
		_, _, err := c.ReadMessage()
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkServer_Hon_WS(b *testing.B) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", benchmarkHonWSHandler)

	eng := hengine.NewEngine(mux)
	srv := hserver.NewServer(eng)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	go func() {
		srv.Serve(addr)
	}()
	defer srv.Shutdown(context.Background())

	time.Sleep(200 * time.Millisecond)

	url := "ws://" + addr + "/ws"

	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		b.Fatalf("Dial failed (Hon): %v", err)
	}
	defer c.Close()

	msg := []byte("Hello Benchmark")

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if err := c.WriteMessage(websocket.TextMessage, msg); err != nil {
			b.Fatal(err)
		}
		_, _, err := c.ReadMessage()
		if err != nil {
			b.Fatal(err)
		}
	}
}
