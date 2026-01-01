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

### 1. HTTP Throughput (High Performance)

Tested with **`wrk`** (100 connections, 2 threads, 10s) on a local environment.
We compared three modes: **Standard** (`net/http`), **Standard + Reuseport**, and **Hon**.

| Metric | Standard (`net/http`) | Std (`reuseport`) | **Hon** | vs Std | vs Reuse |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **Requests/sec** | 129,695 | 211,771 | **232,814** | **+79%** | **+10%** |
| **Throughput** | 56.65MB/s | 92.50MB/s | **105.91MB/s** | **+87%** | **+14%** |

> **Analysis**: 
> - **Standard (`net/http`)**: The baseline performance.
> - **Reuseport**: Significantly improves throughput (+63%) by reducing lock contention on the listener, allowing multiple threads to accept connections.
> - **Hon**: Further improves performance (+10% over Reuseport, +79% over Standard) by utilizing the **Reactor pattern** (Netpoll) and efficient memory pooling, minimizing Garbage Collection and Goroutine scheduling overhead.

### 2. WebSocket Concurrency (Resource Efficiency)

Tested with **15,000 concurrent WebSocket connections** on a single machine (macOS).

| Metric | Standard (`net/http`) | Std (`reuseport`) | **Hon (`Netpoll`)** |
| :--- | :--- | :--- | :--- |
| **Connections** | 15,000 (Stable) | 15,000 (Stable) | **15,000 (Stable)** |
| **Goroutines** | 15,005 | 15,005 | **6** |
| **Architecture** | Thread-per-Conn | Thread-per-Conn | **Event-Driven Reactor** |

> **Key Takeaway**: 
> - **Standard & Reuseport**: Both utilize a **Thread-per-Connection** model, spawning **15,005 goroutines** to handle 15,000 connections. This consumes significant memory (stack space) and increases scheduling overhead.
> - **Hon**: Handles the exact same load with just **6 goroutines** by leveraging the **Reactor pattern**. This represents a **~99.9% reduction** in resource usage, making it ideal for massive concurrency (C10M+).

## 📦 Installation

```bash
go get github.com/DevNewbie1826/hon
```

## 💡 Usage

### Basic Usage

You can run the example server to test different modes:

```bash
# Run in Hon mode (Default)
go run cmd/example/main.go -type hon

# Run in Standard mode (net/http)
go run cmd/example/main.go -type std

# Run in Reuseport mode (net/http + reuseport)
go run cmd/example/main.go -type reuseport
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
