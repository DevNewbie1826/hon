# Hon

[🇰🇷 한국어 (Korean)](README_KO.md) | [🇺🇸 English](README.md)

**Hon** (HTTP-over-Netpoll) is a high-performance HTTP engine adapter based on [CloudWeGo Netpoll](https://github.com/cloudwego/netpoll). It enables standard Go web frameworks (Gin, Chi, Echo) to handle massive concurrency using the **Reactor pattern** with zero code changes.

---

## 🚀 Why Hon?

Traditional Go servers (`net/http`) spawn one goroutine per connection. While Go's scheduler is amazing, maintaining 100,000+ goroutines consumes GBs of RAM and stresses the scheduler.

**Hon** takes a different approach:
- **Reactor Pattern**: Handles operations asynchronously and reuses goroutines from a pool (`gopool`).
- **Zero-Allocation**: Highly optimized for throughput and minimal GC pressure.
- **Zero-Code Integration**: Drop-in replacement for `http.ListenAndServe`.

### ⚡ Key Features
- **Extreme Concurrency**: Handled **25,000+ connections** with only **6 goroutines** in benchmarks.
- **Resource Efficiency**: Maximizes CPU utilization by removing thread-per-conn overhead.
- **True Reactor Client**: Includes a WebSocket client that also runs on the reactor, allowing **0 goroutines per connection**.
- **Standard Compatibility**: Fully compatible with `http.Handler`. Use your favorite router (`chi`, `gin`, `echo`, `mux`).
- **Safety First**: Built-in DoS protection (MaxHeaderSize) and Panic Recovery mechanism.

---

## 📦 Quick Start

### 1. Install
```bash
go get github.com/DevNewbie1826/hon
```

### 2. Basic Usage (Zero Code Change)
Just wrap your existing handler with `hon`.

```go
package main

import (
    "net/http"
    "github.com/DevNewbie1826/hon/pkg/engine"
    "github.com/DevNewbie1826/hon/pkg/server"
)

func main() {
    // 1. Create your standard handler (Gin, Chi, Mux, or just ServeMux)
    mux := http.NewServeMux()
    mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        w.Write([]byte("Hello, Hon!"))
    })

    // 2. Wrap it with Hon Engine
    eng := engine.NewEngine(mux)

    // 3. Serve using Hon Server (instead of http.ListenAndServe)
    server.NewServer(eng).Serve(":8080")
}
```

---

## 🛡️ Best Practices & Safety

To get the most out of Hon, please follow these guidelines.

### ✅ DO (Recommended)

1.  **Use Context Properly**:
    Hon implements `ConnectionState` as a `context.Context`. Use `r.Context()` to handle timeouts and cancellations correctly.
    ```go
    select {
    case <-r.Context().Done():
        return // Connection closed, stop processing
    case <-time.After(time.Second):
        // Work
    }
    ```

2.  **Use the WebSocket Adaptor**:
    For WebSockets, use `hon/pkg/adaptor` to upgrade connections. It seamlessly integrates with the reactor loop.

### 🛑 DON'T (Anti-Patterns)

1.  **Do NOT Block the Handler for too long**:
    Hon uses a **Goroutine Pool** (`gopool`) to reuse goroutines. 
    *   If you block the handler (e.g., long sleep, heavy DB wait), the pool cannot reuse that worker and **must spawn a new goroutine** to handle new requests.
    *   This defeats the purpose of the Reactor pattern and leads to **Goroutine Explosion**, degrading performance to that of a standard server.
    *   **Solution**: For potentially long-blocking tasks, wrap them in a separate goroutine or use efficient async APIs.

2.  **Do NOT Retain WebSocket `payload` without Copying**:
    > [!WARNING]
    > **CRITICAL SAFETY NOTICE**
    
    The `payload` byte slice passed to `OnMessage` is a **Zero-Copy slice** pointing directly to the internal network buffer.
    *   It is valid **ONLY** during the execution of the `OnMessage` function.
    *   If you need to use the data **asynchronously** (e.g., in another goroutine, or storing it in a map), you **MUST** make a copy.

    ```go
    func (h *MyHandler) OnMessage(c net.Conn, op ws.OpCode, payload []byte, fin bool) {
        // ✅ OK: Synchronous processing
        fmt.Println(string(payload)) 
        
        // ❌ BAD: Retaining pointer (Data Corruption Risk!)
        // globalStore = payload 

        // ✅ OK: Making a copy for async/storage
        dataCopy := make([]byte, len(payload))
        copy(dataCopy, payload)
        go processAsync(dataCopy)
    }
    ```

---

## 📊 Performance Benchmark

Tested on macOS M4 (Local).

### 1. HTTP Throughput (RPS)
| Mode | Requests/sec | Improvement |
| :--- | :--- | :--- |
| Standard (`net/http`) | 129,370 | - |
| **Hon** | **237,856** | **+83.8%** |

### 2. WebSocket Efficiency (5,000 Conns)
| Mode | Goroutines | Resource Usage |
| :--- | :--- | :--- |
| Standard (`gorilla`) | 5,006 | 100% (High memory) |
| **Hon Server** | **7** | **0.1% (Extreme efficiency)** |

---

## 📄 License

MIT License
