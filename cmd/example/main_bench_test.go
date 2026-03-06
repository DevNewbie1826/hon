package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	hengine "github.com/DevNewbie1826/hon/pkg/engine"
	hserver "github.com/DevNewbie1826/hon/pkg/server"
)

// newHonServer helper creates a Hon server instance.
func newHonServer(handler http.Handler, addr string) *hserver.Server {
	eng := hengine.NewEngine(handler)
	return hserver.NewServer(eng)
}

// newStdServer helper creates a standard net/http server instance.
func newStdServer(handler http.Handler, addr string) *http.Server {
	return &http.Server{
		Addr:    addr,
		Handler: handler,
	}
}

func setupBenchmarkMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "Welcome! Try /sse endpoint.")
	})
	return mux
}

func reserveLoopbackAddr(tb testing.TB) string {
	tb.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func waitForHTTPServer(tb testing.TB, url string) {
	tb.Helper()

	client := &http.Client{Timeout: 50 * time.Millisecond}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	tb.Fatalf("server at %s did not become reachable", url)
}

func BenchmarkHon(b *testing.B) {
	mux := setupBenchmarkMux()
	addr := reserveLoopbackAddr(b)
	srv := newHonServer(mux, addr)

	go func() {
		if err := srv.Serve(addr); err != nil {
			// log.Println(err)
		}
	}()

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	url := "http://" + addr
	waitForHTTPServer(b, url)

	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := client.Get(url)
		if err != nil {
			b.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	b.StopTimer()
}

func BenchmarkStd(b *testing.B) {
	mux := setupBenchmarkMux()
	addr := reserveLoopbackAddr(b)
	srv := newStdServer(mux, addr)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			// log.Println(err)
		}
	}()

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	url := "http://" + addr
	waitForHTTPServer(b, url)

	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := client.Get(url)
		if err != nil {
			b.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	b.StopTimer()
}
