package server

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/DevNewbie1826/hon/pkg/engine"
)

func TestServer_Integration(t *testing.T) {
	// 1. Find a free port
	// 0 port means OS chooses a free port, but netpoll/reuseport might need explicit port or handling.
	// Let's try port 0.
	addr := "localhost:18888" // Use a likely free port or 0 if supported well

	// 2. Setup Server
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Integration Test"))
	})
	eng := engine.NewEngine(handler)
	srv := NewServer(eng)

	// 3. Start Server in a goroutine
	go func() {
		if err := srv.Serve(addr); err != nil {
			// This might fail if test runs multiple times quickly on same port or port in use.
			// Ideally we use listener on port 0, get addr, then serve.
			// But Serve() creates listener internally.
			t.Logf("Server stopped: %v", err)
		}
	}()

	// Give server some time to start
	time.Sleep(100 * time.Millisecond)

	// 4. Send Request using standard http client
	resp, err := http.Get("http://" + addr)
	if err != nil {
		t.Fatalf("http.Get failed: %v", err)
	}
	defer resp.Body.Close()

	// 5. Verify Response
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200 OK, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "Integration Test" {
		t.Errorf("Expected body 'Integration Test', got '%s'", string(body))
	}

	// 6. Shutdown (Not fully implemented in our Server struct to stop Serve loop easily from outside without ctx)
	// Currently srv.Shutdown(ctx) is implemented but srv.Serve() blocks.
	// The test ends, killing the goroutine eventually.
}
