package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"syscall"
	"time"

	hserver "github.com/DevNewbie1826/hon/pkg/server"
	hengine "github.com/DevNewbie1826/hon/pkg/engine"
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
	_ = SetUlimit()

	// pprof for monitoring
	go func() {
		log.Printf("Starting pprof on localhost:6060")
		_ = http.ListenAndServe("localhost:6060", nil)
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
	if *serverType == "std" {
		log.Printf("Starting Standard net/http server on %s", addr)
		_ = http.ListenAndServe(addr, mux)
	} else {
		log.Printf("Starting Hon server on %s", addr)
		eng := hengine.NewEngine(mux)
		srv := hserver.NewServer(eng)
		_ = srv.Serve(addr)
	}
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "Hon Server Running\nEndpoints: /ws-hon, /ws-std, /sse\n")
}

// --- Hon Optimized WebSocket ---
type HonWSHandler struct {
	hws.DefaultHandler
}

func (h *HonWSHandler) OnMessage(c net.Conn, op ws.OpCode, payload []byte, fin bool) {
	_ = wsutil.WriteServerMessage(c, op, payload)
}

func honWebSocketHandler(w http.ResponseWriter, r *http.Request) {
	_ = hws.Upgrade(w, r, &HonWSHandler{})
}

// --- Standard Gorilla WebSocket ---
var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

func gorillaStdHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	go func() {
		defer conn.Close()
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			_ = conn.WriteMessage(mt, msg)
		}
	}()
}

func sseHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	for i := 0; i < 10; i++ {
		fmt.Fprintf(w, "data: Message %d\n\n", i)
		w.(http.Flusher).Flush()
		time.Sleep(1 * time.Second)
	}
}