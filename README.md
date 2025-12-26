# Hon

[🇰🇷 한국어 (Korean)](README_KO.md) | [🇺🇸 English](README.md)

**Hon** is a high-performance HTTP engine adapter based on [CloudWeGo Netpoll](https://github.com/cloudwego/netpoll). **Hon** stands for "HTTP-over-Netpoll".

It allows you to run existing popular Go web frameworks like Gin, Chi, and Echo on top of Netpoll without code changes, enabling high-performance I/O processing based on the Reactor pattern (epoll/kqueue) while maintaining the standard `net/http` interface.

## 🚀 Key Features

- **Extreme Concurrency**: Handle **25,000+ concurrent connections** with only **6 goroutines**. (99.9% reduction compared to standard `net/http`)
- **Standard Compatibility**: Fully supports `http.Handler` interface, compatible with `chi`, `gin`, `echo`, `mux`, etc.
- **Resource Efficiency**: Maximizes CPU utilization by eliminating the thread-per-connection overhead.
- **Memory Management**: Utilizes Netpoll's `mcache` for high-performance memory allocation and reuse, ensuring stable operation under heavy load.
- **Reactor Mode WebSocket**: Process WebSocket messages directly in the event loop without spawning goroutines per connection.
- **SSE Support**: Supports Server-Sent Events streaming via `http.Flusher`.

## 📊 Performance Benchmark

Tested with **25,000 concurrent WebSocket connections** on a single machine (macOS).

| Metric | Standard (`net/http`) | **Hon (`Netpoll`)** | Improvement |
| :--- | :--- | :--- | :--- |
| **Goroutines** | 25,000+ (Crashed at ~18k) | **6** | **99.9% Reduction** |
| **Stability** | Failed to sustain 25k | **Stable at 25k** | **High Availability** |
| **Architecture** | Thread-per-Connection | **Event-Driven Reactor** | **Non-blocking I/O** |

> **Note on Memory**: Hon prioritizes throughput and latency stability. It utilizes `mcache` (Netpoll's memory cache) to minimize allocation overhead during high concurrency, maintaining a consistent memory footprint (approx. 495MB for 25k connections in our test).

## 📦 Installation

```bash
go get github.com/DevNewbie1826/hon
```

## 💡 Usage

### Basic Usage with Optimization

```go
package main

import (
	"log"
	"net/http"
	"time"

	"github.com/DevNewbie1826/hon/pkg/engine"
	"github.com/DevNewbie1826/hon/pkg/server"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Hello, Hon!"))
	})

	// Optimize buffer size for high concurrency (Default: 4KB)
	eng := engine.NewEngine(mux, 
		engine.WithBufferSize(1024),
		engine.WithRequestTimeout(5*time.Second),
	)

	srv := server.NewServer(eng,
		server.WithReadTimeout(10*time.Second),
		server.WithWriteTimeout(10*time.Second),
	)

	log.Println("Server listening on :1826")
	if err := srv.Serve(":1826"); err != nil {
		log.Fatal(err)
	}
}
```

### Event-Driven WebSocket (Zero-Goroutine)

The most efficient way to handle WebSockets without spawning a goroutine per connection.
Recommended library: `github.com/gobwas/ws`

```go
func wsHandler(w http.ResponseWriter, r *http.Request) {
    // Upgrade connection...
    if hijacker, ok := w.(adaptor.Hijacker); ok {
        hijacker.SetReadHandler(func(c net.Conn, rw *bufio.ReadWriter) error {
            // This callback runs in the Netpoll worker pool.
            // Do NOT use blocking loops here.
            
            // Example using gobwas/wsutil
            msg, op, err := wsutil.ReadClientData(rw)
            if err != nil {
                return err
            }
            return wsutil.WriteServerMessage(rw, op, msg)
        })
    }
}
```

## 🛠 Stress Test

You can verify the performance using the included stress test tool.

```bash
# Test with 10,000 connections held for 30 seconds
go run ws_stress_config.go -c 10000 -hold 30s
```

## 🏗 Architecture

- **Server**: Manages Netpoll's EventLoop and accepts TCP connections.
- **Engine**: Manages connection state (`ConnectionState`) and buffer pools, dispatching requests to the handler.
- **Adaptor**: Bridges Netpoll's raw connection and standard `net/http` objects.

## 🤝 Contributing

Bug reports and feature suggestions are welcome. Please submit an issue or PR.

## 📄 License

MIT License
