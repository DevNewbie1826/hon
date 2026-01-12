# Hon

[🇰🇷 한국어 (Korean)](README_KO.md) | [🇺🇸 English](README.md)

**Hon** (HTTP-over-Netpoll) is a high-performance HTTP engine adapter based on [CloudWeGo Netpoll](https://github.com/cloudwego/netpoll). It enables standard Go web frameworks (Gin, Chi, Echo) to handle massive concurrency using the **Reactor pattern** with zero code changes.

## 🚀 Key Features

- **Extreme Concurrency**: Handle **25,000+ connections** with only **6 goroutines**.
- **Resource Efficiency**: Maximizes CPU utilization by eliminating the Thread-per-Connection overhead.
- **True Reactor Client**: Provides a WebSocket client that uses the same Reactor pattern, achieving **0 goroutines per connection**.
- **Standard Compatibility**: Fully supports the `http.Handler` interface, allowing you to use existing routers like `chi`, `gin`, `echo`, `mux`, etc., without any code changes.
- **Memory Management**: Utilizes Netpoll's high-performance memory cache (`mcache`) to minimize allocation/deallocation costs and ensure stable performance under heavy traffic.
- **Zero-Allocation**: 
  - **HTTP Parser**: Custom Zero-Alloc parser achieving **0 allocs/op** (Benchmark verified).
  - **WebSocket**: Fully pooled assembly, compression, and masking.
  - **Adaptor**: Stack-based sniffing for `Content-Type`, eliminating heap allocations.

## 📊 Performance Benchmark (Hon vs Standard)

Tested on macOS M4 (Local).

### 1. HTTP Throughput (RPS)
| Mode | Requests/sec | Improvement |
| :--- | :--- | :--- |
| Standard (`net/http`) | 129,616 | - |
| **Hon** | **233,404** | **+80%** |

> **Analysis**: 
> - **Hon**: Outperforms the standard Go server by **80%** by utilizing the **Reactor pattern** and efficient memory pooling.

### 2. Zero-Allocation Parser (Micro-Benchmark)
| Component | Metric | Hon | Standard (`strconv`) |
| :--- | :--- | :--- | :--- |
| **Chunked Parsing** | **Allocations** | **0 allocs/op** | 1+ allocs/op |
| **Content-Length** | **Allocations** | **0 allocs/op** | 0+ allocs/op |
| **Parsing Speed** | **Time/Op** | **49.8 ns** | 71.7 ns |

### 3. WebSocket Efficiency (15,000 Conns)
| Mode | Goroutines | Resource Usage |
| :--- | :--- | :--- |
| Standard (`gorilla`) | 15,005 | 100% (1 per conn) |
| **Hon Server** | **6** | **0.04% (Reactor)** |

### 4. Client Scalability (10,000 Conns)
| Metric | Hon Client | Standard Client |
| :--- | :--- | :--- |
| **Goroutines** | **~4 (Total)** | 20,000+ |
| **Connect Time** | **0.48s** | ~5.0s+ |
| **Allocations** | **0 allocs/op** | High |

> **Key Takeaway**: 
> - **Hon Client**: Uses Netpoll's global poller to manage thousands of connections without spawning any goroutines per connection. Ideal for massive load testing or gateway services.

## 📦 Quick Start

```bash
# 1. Install
go get github.com/DevNewbie1826/hon

# 2. Run Example
go run cmd/example/main.go -type hon
```

## 💡 Usage Example

### Server
```go
func main() {
    mux := http.NewServeMux()
    
    // 1. Standard HTTP Handler (Compatible with Gin, Chi, Echo)
    mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        w.Write([]byte("Hello, Hon!"))
    })

    // 2. Hon Optimized WebSocket
    mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
        adaptor.Upgrade(w, r, &MyWSHandler{})
    })

    eng := engine.NewEngine(mux)
    server.NewServer(eng).Serve(":1826")
}

type MyWSHandler struct { websocket.DefaultHandler }
func (h *MyWSHandler) OnMessage(c net.Conn, op ws.OpCode, payload []byte, fin bool) {
    // Event-driven callback without per-connection goroutine
    wsutil.WriteServerMessage(c, op, payload)
}
```

### Client
```go
// Connect to WebSocket Server
// No goroutine is spawned for this connection!
err := websocket.Dial("ws://localhost:1826/ws", &MyClientHandler{})

type MyClientHandler struct { websocket.DefaultHandler }
func (h *MyClientHandler) OnMessage(c net.Conn, op ws.OpCode, p []byte, fin bool) {
    fmt.Printf("Received: %s\n", string(p))
}
```

## 📄 License
MIT License
