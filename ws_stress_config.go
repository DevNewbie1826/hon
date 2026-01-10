package main

import (
	"flag"
	"log"
	"net/url"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

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

// go run ws_stress_config.go -c 10000 -hold 30s
func main() {
	_ = SetUlimit()
	conns := flag.Int("c", 10000, "Number of concurrent connections")
	path := flag.String("path", "/ws-gobwas-low", "WebSocket endpoint path")
	host := flag.String("host", "localhost:1826", "Server host and port")
	hold := flag.Duration("hold", 20*time.Second, "Time to hold each connection")
	echo := flag.Bool("echo", false, "Enable echo (send/receive) mode")
	flag.Parse()

	u := url.URL{Scheme: "ws", Host: *host, Path: *path}
	log.Printf("Starting %d connections to %s (Echo: %v)...", *conns, u.String(), *echo)

	var wg sync.WaitGroup
	var connected int64
	// Extreme dial concurrency
	dialSem := make(chan struct{}, 1000)

	// Monitor reporter
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			current := atomic.LoadInt64(&connected)
			if current > 0 {
				log.Printf("Current active connections: %d", current)
			}
		}
	}()

	for i := 0; i < *conns; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			dialSem <- struct{}{}
			c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
			<-dialSem

			if err != nil {
				return
			}
			atomic.AddInt64(&connected, 1)
			defer func() {
				c.Close()
				atomic.AddInt64(&connected, -1)
			}()

			if *echo {
				deadline := time.Now().Add(*hold)
				for time.Now().Before(deadline) {
					msg := []byte("hello hon")
					if err := c.WriteMessage(websocket.TextMessage, msg); err != nil {
						return
					}
					_, resp, err := c.ReadMessage()
					if err != nil {
						return
					}
					if string(resp) != string(msg) {
						log.Printf("Mismatch: sent %s, got %s", msg, resp)
					}
					time.Sleep(1 * time.Second)
				}
			} else {
				time.Sleep(*hold)
			}
		}(i)
	}

	wg.Wait()
	log.Println("Stress test finished.")
}
