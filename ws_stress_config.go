package main

import (
	"flag"
	"log"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/DevNewbie1826/hon/pkg/websocket"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

var connected int64

func SetUlimit() error {
	var rLimit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit); err != nil {
		return err
	}
	rLimit.Cur = rLimit.Max
	return syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rLimit)
}

type StressHandler struct {
	websocket.DefaultHandler
	echo bool
}

func (h *StressHandler) OnOpen(c net.Conn) {
	atomic.AddInt64(&connected, 1)
	if h.echo {
		// Start the loop
		_ = wsutil.WriteClientMessage(c, ws.OpText, []byte("hello hon"))
	}
}

func (h *StressHandler) OnMessage(c net.Conn, op ws.OpCode, p []byte, fin bool) {
	if h.echo {
		// Simple verification
		// In high load, logging every mismatch might be too much, but good for correctness check.
		// Schedule next message
		time.AfterFunc(1*time.Second, func() {
			_ = wsutil.WriteClientMessage(c, ws.OpText, []byte("hello hon"))
		})
	}
}

func (h *StressHandler) OnClose(c net.Conn, err error) {
	atomic.AddInt64(&connected, -1)
}

func main() {
	_ = SetUlimit()
	conns := flag.Int("c", 10000, "Number of concurrent connections")
	path := flag.String("path", "/ws-gobwas-low", "WebSocket endpoint path")
	host := flag.String("host", "localhost:1826", "Server host and port")
	hold := flag.Duration("hold", 20*time.Second, "Time to hold test")
	echo := flag.Bool("echo", false, "Enable echo (send/receive) mode")
	flag.Parse()
	log.Printf("Debug Path: %s", *path)

	url := "ws://" + *host + *path
	log.Printf("Starting %d connections to %s (Echo: %v, Mode: Hon Reactor)...", *conns, url, *echo)

	// Monitor reporter
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			current := atomic.LoadInt64(&connected)
			log.Printf("Active connections: %d | Client Goroutines: %d", current, runtime.NumGoroutine())
		}
	}()

	// Dial Loop
	// To avoid file descriptor exhaustion bursts, we throttle dialing.
	dialSem := make(chan struct{}, 500) // Max 500 concurrent dials
	var dialWg sync.WaitGroup

	handler := &StressHandler{echo: *echo}

	start := time.Now()

	for i := 0; i < *conns; i++ {
		dialSem <- struct{}{}
		dialWg.Add(1)
		
		go func() {
			defer func() { <-dialSem; dialWg.Done() }()
			
			// Hon's Dial is non-blocking regarding the connection lifecycle (Reactor),
			// but Dial() itself blocks until handshake is done.
			err := websocket.Dial(url, handler)
			if err != nil {
				log.Printf("Dial failed: %v", err) // Too noisy
				return
			}
		}()
	}

	// Wait for all dials to complete
	dialWg.Wait()
	dialTime := time.Since(start)
	log.Printf("All %d connection attempts finished in %v", *conns, dialTime)
	
	// Hold connections
	log.Printf("Holding connections for %v...", *hold)
	time.Sleep(*hold)
	
	log.Println("Stress test finished. Exiting...")
	// When main exits, all connections (and reactor) will be closed.
}