package main

import (
	"context"
	"fmt"
	"io"
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

func BenchmarkHon(b *testing.B) {
	mux := setupBenchmarkMux()
	addr := ":1827"
	srv := newHonServer(mux, addr)

	go func() {
		if err := srv.Serve(addr); err != nil {
			// log.Println(err)
		}
	}()
	// Give server some time to start
	time.Sleep(100 * time.Millisecond)

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	url := "http://127.0.0.1" + addr

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
	addr := ":1829"
	srv := newStdServer(mux, addr)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			// log.Println(err)
		}
	}()
	// Give server some time to start
	time.Sleep(100 * time.Millisecond)

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	url := "http://127.0.0.1" + addr

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
