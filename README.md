# Hon

[🇰🇷 한국어 (Korean)](README_KO.md) | [🇺🇸 English](README.md)

**Hon** (HTTP-over-Netpoll) is a high-performance HTTP engine adapter based on [CloudWeGo Netpoll](https://github.com/cloudwego/netpoll). It enables standard Go web frameworks (Gin, Chi, Echo) to handle massive concurrency using the **Reactor pattern** with zero code changes.

## 🚀 Key Features

- **Extreme Concurrency**: Handle **25,000+ connections** with only **6 goroutines**.
- **Resource Efficiency**: Maximizes CPU utilization by eliminating the Thread-per-Connection overhead.
- **Standard Compatibility**: Fully supports the `http.Handler` interface, allowing you to use existing routers like `chi`, `gin`, `echo`, `mux`, etc., without any code changes.
- **Memory Management**: Utilizes Netpoll's high-performance memory cache (`mcache`) to minimize allocation/deallocation costs and ensure stable performance under heavy traffic.
- **Zero-Allocation**: Custom WebSocket parser achieving **0 allocs/op**.
- **Streaming**: Optimized for large data with **64KB chunked processing**.

## 📊 Performance Benchmark (Hon vs Standard)

Tested on macOS M4 (Local).

### 1. HTTP Throughput (RPS)
| Mode | Requests/sec | Improvement |
| :--- | :--- | :--- |
| Standard (`net/http`) | 129,616 | - |
| **Hon** | **233,404** | **+80%** |

> **Analysis**: 
> - **Hon**: Outperforms the standard Go server by **80%** by utilizing the **Reactor pattern** and efficient memory pooling.

### 2. WebSocket Efficiency (15,000 Conns)
| Mode | Goroutines | Resource Usage |
| :--- | :--- | :--- |
| Standard (`gorilla`) | 15,005 | 100% (1 per conn) |
| **Hon** | **6** | **0.04% (Reactor)** |

> **Key Takeaway**: 
> - **Standard**: Spawns **1 goroutine per connection**, creating 15,005 goroutines. This leads to massive scheduling overhead and GC pressure.
> - **Hon**: Handles 15,000 connections with just **6 goroutines**. While initial memory usage (RSS) is slightly higher due to Netpoll's aggressive `mcache` and `LinkBuffer` pooling, it eliminates runtime GC overhead and scales linearly to C10M.

## 📦 Quick Start

```bash
# 1. Install
go get github.com/DevNewbie1826/hon

# 2. Run Example
go run cmd/example/main.go -type hon
```

## 💡 Usage Example

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

## 📄 License
MIT License