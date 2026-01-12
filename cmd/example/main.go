package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	hengine "github.com/DevNewbie1826/hon/pkg/engine"
	hserver "github.com/DevNewbie1826/hon/pkg/server"
	hws "github.com/DevNewbie1826/hon/pkg/websocket"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/gorilla/websocket"
)

func SetUlimit() error {
	var rLimit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit); err != nil {
		return err
	}
	rLimit.Cur = rLimit.Max
	return syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rLimit)
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	if err := SetUlimit(); err != nil {
		log.Printf("Warning: Failed to set ulimit: %v", err)
	}

	// pprof for monitoring
	go func() {
		log.Printf("Starting pprof on localhost:6060")
		if err := http.ListenAndServe("localhost:6060", nil); err != nil {
			log.Printf("pprof failed: %v", err)
		}
	}()

	serverType := flag.String("type", "hon", "Server type: hon, std")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/", rootHandler)

	// 1. Hon Optimized WebSocket (Reactor Mode)
	mux.HandleFunc("/ws-hon", honWebSocketHandler)

	// 2. Standard Gorilla WebSocket (Thread-per-Conn Mode)
	mux.HandleFunc("/ws-std", gorillaStdHandler)

	// 3. SSE Example
	mux.HandleFunc("/sse", sseHandler)

	addr := ":1826"
	
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	if *serverType == "std" {
		log.Printf("Starting Standard net/http server on %s", addr)
		srv := &http.Server{
			Addr:    addr,
			Handler: mux,
		}

		go func() {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("Standard server failed: %v", err)
			}
		}()

		<-quit
		log.Println("Shutting down server...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("Server forced to shutdown: %v", err)
		}
		log.Println("Server exiting")

	} else {
		log.Printf("Starting Hon server on %s", addr)
		eng := hengine.NewEngine(mux)
		srv := hserver.NewServer(eng)
		
		go func() {
			if err := srv.Serve(addr); err != nil {
				log.Fatalf("Hon server failed: %v", err)
			}
		}()

		<-quit
		log.Println("Shutting down server...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("Server forced to shutdown: %v", err)
		}
		log.Println("Server exiting")
	}
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	if _, err := fmt.Fprint(w, "Hon Server Running\nEndpoints: /ws-hon, /ws-std, /sse\n"); err != nil {
		log.Printf("Root handler write failed: %v", err)
	}
}

// --- Hon Optimized WebSocket ---
type HonWSHandler struct {
	hws.DefaultHandler
}

func (h *HonWSHandler) OnMessage(c net.Conn, op ws.OpCode, payload []byte, fin bool) {
	if err := wsutil.WriteServerMessage(c, op, payload); err != nil {
		log.Printf("Hon write failed: %v", err)
	}
}

func honWebSocketHandler(w http.ResponseWriter, r *http.Request) {
	// Upgrade with security options
	err := hws.Upgrade(w, r, &HonWSHandler{},
		hws.WithCheckOrigin(func(r *http.Request) bool {
			return true // Allow all for example, but explicit
		}),
	)
	if err != nil {
		log.Printf("Hon upgrade failed: %v", err)
	}
}

// --- Standard Gorilla WebSocket ---
// Note: In production, implement proper origin checking
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func gorillaStdHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Gorilla upgrade failed: %v", err)
		return
	}
	go func() {
		defer conn.Close()
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
						if err := conn.WriteMessage(mt, msg); err != nil {
							log.Printf("Gorilla write failed: %v", err)
							return
						}		}
	}()
}

func sseHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// Handle client disconnect
	ctx := r.Context()

	for i := 0; i < 10; i++ {
				select {
				case <-ctx.Done():
					log.Println("SSE client disconnected")
					return
				case <-ticker.C:
					fmt.Fprintf(w, "data: Message %d\n\n", i)
					flusher.Flush()
				}	}
}