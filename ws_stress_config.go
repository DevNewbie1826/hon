package main

import (
	"flag"
	"log"
	"net/url"
	"sync"
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
	flag.Parse()

	u := url.URL{Scheme: "ws", Host: *host, Path: *path}
	log.Printf("Starting %d connections to %s...", *conns, u.String())

	var wg sync.WaitGroup
	// Limit dial concurrency to avoid overwhelming the OS/Network
	dialSem := make(chan struct{}, 200)

	for i := 0; i < *conns; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			dialSem <- struct{}{}
			c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
			<-dialSem

			if err != nil {
				if id < 5 {
					log.Printf("Dial failed: %v", err)
				}
				return
			}
			defer c.Close()

			// Keep the connection alive
			time.Sleep(*hold)
		}(i)

		// Slight delay to avoid massive burst of CPU usage during dial
		if i%200 == 0 && i > 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}

	wg.Wait()
	log.Println("Stress test finished.")
}
