package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof" // Import for profiling
	"time"

	"github.com/DevNewbie1826/hon/pkg/adaptor"
	hengine "github.com/DevNewbie1826/hon/pkg/engine"
	hserver "github.com/DevNewbie1826/hon/pkg/server"

	"github.com/cloudwego/netpoll"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/gorilla/websocket"
)

func main() {
	// Set up logging to standard output
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// Start pprof server for profiling in a separate goroutine
	go func() {
		pprofAddr := "localhost:6060"
		log.Printf("Starting pprof server on %s", pprofAddr)
		if err := http.ListenAndServe(pprofAddr, nil); err != nil {
			log.Printf("pprof server failed: %v", err)
		}
	}()

	serverType := flag.String("type", "hon", "Server type: hon, std")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/", rootHandler)
	mux.HandleFunc("/file", fileHandler) // Restore file handler route
	mux.HandleFunc("/sse", sseHandler)   // Restore SSE handler route

	// 1. gobwas/ws Low-Level (Event-Driven) - Best Performance
	mux.HandleFunc("/ws-gobwas-low", gobwasLowLevelHandler)

	// 2. gobwas/ws High-Level (Event-Driven) - Easy & Efficient
	mux.HandleFunc("/ws-gobwas-high", gobwasHighLevelHandler)

	// 3. gorilla/websocket (Standard Loop) - Compatibility Mode
	mux.HandleFunc("/ws-gorilla-std", gorillaStdHandler)

	// 4. gorilla/websocket (Event-Driven) - Reactor Mode with Gorilla
	mux.HandleFunc("/ws-gorilla-event", gorillaEventHandler)

	addr := ":1826"

	if *serverType == "std" {
		std(mux, addr)
		return
	}

	hon(mux, addr)
}

func std(mux http.Handler, addr string) {
	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
	}
	log.Printf("Standard net/http server starting on %s...", addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("Standard server failed: %v", err)
	}
}

func hon(mux http.Handler, addr string) {
	// 1KB buffer size for optimization
	eng := hengine.NewEngine(mux, hengine.WithBufferSize(1024))

	srv := hserver.NewServer(eng,
		hserver.WithReadTimeout(10*time.Second),
		hserver.WithWriteTimeout(10*time.Second),
	)

	log.Printf("Starting Hon server on %s", addr)
	if err := srv.Serve(addr); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, `Welcome to Hon WebSocket Examples!
Available endpoints:
1. /ws-gobwas-low    (Low-Level Event-Driven)
2. /ws-gobwas-high   (High-Level Event-Driven using wsutil)
3. /ws-gorilla-std   (Standard Loop in Goroutine)
4. /ws-gorilla-event (Gorilla on Reactor Pattern)
5. /file             (File Serving)
6. /sse              (Server-Sent Events)
`)
}

// sseHandler streams Server-Sent Events.
func sseHandler(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Disable write deadline for SSE as it is a long-lived connection.
	// We use http.ResponseController (Go 1.20+) to control the underlying connection.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			log.Println("Client disconnected from SSE")
			return
		case t := <-ticker.C:
			fmt.Fprintf(w, "data: Server time is %v\r\n\r\n", t)
			flusher.Flush()
		}
	}
}

// fileHandler serves a file using http.ServeFile.
func fileHandler(w http.ResponseWriter, r *http.Request) {
	// Serve the current main.go file as an example
	http.ServeFile(w, r, "cmd/example/main.go")
}

// -------------------------------------------------------------------------
// 1. gobwas/ws Low-Level (Event-Driven)
// Direct control over frames and buffers. Maximum performance.
// -------------------------------------------------------------------------
func gobwasLowLevelHandler(w http.ResponseWriter, r *http.Request) {
	_, _, _, err := ws.UpgradeHTTP(r, w) // conn is unused directly, use _
	if err != nil {
		log.Printf("[gobwas-low] upgrade error: %v", err)
		return
	}
	//log.Printf("[gobwas-low] Connected: %s", r.RemoteAddr)

	if hijacker, ok := w.(adaptor.Hijacker); ok {
		hijacker.SetReadHandler(func(c net.Conn, rw *bufio.ReadWriter) error {
			// Check if connection is active
			if !c.(netpoll.Connection).IsActive() {
				return io.EOF
			}

			// Read Header
			header, err := ws.ReadHeader(rw.Reader)
			if err != nil {
				if err != io.EOF {
					log.Printf("[gobwas-low] ReadHeader error: %v", err)
				}
				return err
			}

			// Read Payload
			payload := make([]byte, header.Length)
			_, err = io.ReadFull(rw.Reader, payload)
			if err != nil {
				log.Printf("[gobwas-low] ReadFull error: %v", err)
				return err
			}

			// Unmask
			if header.Masked {
				ws.Cipher(payload, header.Mask, 0)
			}

			// Echo Logic
			switch header.OpCode {
			case ws.OpText, ws.OpBinary:
				respHeader := ws.Header{
					Fin:    true,
					OpCode: header.OpCode,
					Length: int64(len(payload)),
				}
				if err := ws.WriteHeader(rw.Writer, respHeader); err != nil {
					return err
				}
				if _, err := rw.Writer.Write(payload); err != nil {
					return err
				}
				if err := rw.Writer.Flush(); err != nil {
					return err
				}
			case ws.OpClose:
				return io.EOF
			}

			return nil
		})
	}
}

// -------------------------------------------------------------------------
// 2. gobwas/ws High-Level (Event-Driven)
// Uses wsutil for convenience. Still Event-Driven.
// -------------------------------------------------------------------------
func gobwasHighLevelHandler(w http.ResponseWriter, r *http.Request) {
	_, _, _, err := ws.UpgradeHTTP(r, w) // conn is unused directly, use _
	if err != nil {
		log.Printf("[gobwas-high] upgrade error: %v", err)
		return
	}
	//log.Printf("[gobwas-high] Connected: %s", r.RemoteAddr)

	if hijacker, ok := w.(adaptor.Hijacker); ok {
		hijacker.SetReadHandler(func(c net.Conn, rw *bufio.ReadWriter) error {
			if !c.(netpoll.Connection).IsActive() {
				return io.EOF
			}

			msg, op, err := wsutil.ReadClientData(rw) // Use rw (*bufio.ReadWriter)
			if err != nil {
				if err != io.EOF {
					log.Printf("[gobwas-high] Read error: %v", err)
				}
				return err
			}

			err = wsutil.WriteServerMessage(rw, op, msg) // Use rw (*bufio.ReadWriter)
			if err != nil {
				log.Printf("[gobwas-high] Write error: %v", err)
				return err
			}
			rw.Writer.Flush()

			return nil
		})
	}
}

// -------------------------------------------------------------------------
// 3. gorilla/websocket (Standard Loop)
// Compatible with existing code. Uses one goroutine per connection.
// -------------------------------------------------------------------------
var gorillaUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func gorillaStdHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := gorillaUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[gorilla-std] upgrade error: %v", err)
		return
	}
	//log.Printf("[gorilla-std] Connected: %s", r.RemoteAddr)

	// Traditional Loop in a separate goroutine
	go func() {
		defer conn.Close()
		for {
			messageType, message, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
					log.Printf("[gorilla-std] Read error: %v", err)
				}
				return
			}
			if err := conn.WriteMessage(messageType, message); err != nil {
				log.Printf("[gorilla-std] Write error: %v", err)
				return
			}
		}
	}()
}

// -------------------------------------------------------------------------
// 4. gorilla/websocket (Event-Driven)
// Using Gorilla library on Hon's Reactor Pattern. No per-conn goroutine.
// -------------------------------------------------------------------------
func gorillaEventHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := gorillaUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[gorilla-event] upgrade error: %v", err)
		return
	}
	//log.Printf("[gorilla-event] Connected: %s", r.RemoteAddr)

	if hijacker, ok := w.(adaptor.Hijacker); ok {
		hijacker.SetReadHandler(func(c net.Conn, rw *bufio.ReadWriter) error {
			// netpoll calls this when data is available.
			// conn.ReadMessage() will read from the socket (mostly non-blocking or short blocking).
			messageType, message, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
					log.Printf("[gorilla-event] Read error: %v", err)
				}
				return err
			}

			if err := conn.WriteMessage(messageType, message); err != nil {
				log.Printf("[gorilla-event] Write error: %v", err)
				return err
			}
			return nil
		})
	}
}