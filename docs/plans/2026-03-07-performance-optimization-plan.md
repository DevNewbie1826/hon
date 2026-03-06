# Performance Optimization Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Measure and reduce the highest-confidence performance costs in Hon's HTTP and WebSocket hot paths without widening the correctness risk surface.

**Architecture:** Start with benchmark-backed, low-risk changes in `pkg/engine`, `pkg/websocket`, and test harnesses. Only move to medium-risk path simplification after the low-risk tasks produce hard evidence that the targeted paths dominate throughput or allocation cost.

**Tech Stack:** Go, CloudWeGo Netpoll, standard `net/http`, Go benchmark tooling, `staticcheck`, race detector.

---

### Task 1: Lock Down Measurement Before Optimizing

**Files:**
- Modify: `pkg/engine/benchmark_test.go` (new benchmark file if needed)
- Modify: `pkg/websocket/benchmark_test.go`
- Modify: `cmd/example/benchmark_comparison_test.go`

**Step 1: Write the failing benchmark coverage**

Add focused benchmarks for the paths we want to change:

```go
func BenchmarkEngineServeHTTP_SmallKeepAlive(b *testing.B) { ... }
func BenchmarkClientHandshake_ParseResponse(b *testing.B) { ... }
func BenchmarkWriteMessage_TextUncompressed(b *testing.B) { ... }
```

**Step 2: Run benchmark to confirm baseline**

Run:

```bash
go test -bench=EngineServeHTTP -benchmem ./pkg/engine
go test -bench='ClientHandshake|WriteMessage' -benchmem ./pkg/websocket
```

Expected:
- The benchmarks compile and produce stable baseline numbers.
- No correctness changes yet.

**Step 3: Write minimal implementation**

- Add helper fixtures for reusable request/frame payloads.
- Avoid logging or random data in benchmark hot loops.

**Step 4: Run benchmark again**

Run the same benchmark commands and record the baseline in the task notes.

**Step 5: Commit**

```bash
git add pkg/engine pkg/websocket cmd/example
git commit -m "test: add focused performance benchmarks"
```

### Task 2: Remove Unconditional Scheduler Yield From HTTP Hot Path

**Files:**
- Modify: `pkg/engine/engine.go`
- Modify: `pkg/engine/engine_test.go`
- Test: `pkg/engine/benchmark_test.go`

**Step 1: Write the failing regression/perf test**

Add a benchmark-oriented guard that exercises pipelined requests without relying on `runtime.Gosched()`.

```go
func BenchmarkEngineServeHTTP_Pipelined(b *testing.B) { ... }
```

If a behavior regression test is needed, add:

```go
func TestEngine_ServeConn_PipelinedRequestsRemainOrdered(t *testing.T) { ... }
```

**Step 2: Run benchmark/test before code changes**

Run:

```bash
go test ./pkg/engine -run TestEngine_ServeConn_PipelinedRequestsRemainOrdered -count=1
go test -bench='EngineServeHTTP|Pipelined' -benchmem ./pkg/engine
```

Expected:
- Existing behavior remains green.
- Benchmarks show the current baseline with `runtime.Gosched()`.

**Step 3: Write minimal implementation**

Remove the unconditional scheduler yield from `serveHTTP`:

```go
for {
    if !conn.IsActive() { ... }
    ...
}
```

If fairness still needs protection, only reintroduce a yield under a demonstrable starvation condition.

**Step 4: Run verification**

Run:

```bash
go test ./pkg/engine -count=1
go test -race ./pkg/engine -count=1
go test -bench='EngineServeHTTP|Pipelined' -benchmem ./pkg/engine
```

Expected:
- Tests still pass.
- Benchmarks improve or stay neutral; regressions must be documented.

**Step 5: Commit**

```bash
git add pkg/engine/engine.go pkg/engine/engine_test.go pkg/engine/benchmark_test.go
git commit -m "perf: remove unconditional scheduler yield from engine"
```

### Task 3: Reduce Handshake Parser Allocation In WebSocket Client

**Files:**
- Modify: `pkg/websocket/client.go`
- Modify: `pkg/websocket/client_test.go`
- Test: `pkg/websocket/benchmark_test.go`

**Step 1: Write the failing benchmark**

Add a benchmark for response header validation:

```go
func BenchmarkValidateHandshakeResponse(b *testing.B) {
    lines := []string{ ... }
    for i := 0; i < b.N; i++ {
        _ = validateHandshakeResponse(lines, secKey)
    }
}
```

**Step 2: Run benchmark before implementation**

Run:

```bash
go test -bench='Handshake|ValidateHandshakeResponse' -benchmem ./pkg/websocket
```

Expected:
- Baseline allocs for the current string/split path are recorded.

**Step 3: Write minimal implementation**

Replace `string(peekBuf[:idx]) + strings.Split` with line scanning that minimizes temporary allocations:

```go
headerBytes := peekBuf[:idx]
for len(headerBytes) > 0 {
    lineEnd := bytes.Index(headerBytes, []byte("\r\n"))
    ...
}
```

Keep the externally visible semantics the same:
- correct `101` check
- correct duplicate `Connection` handling
- correct `Sec-WebSocket-Accept` validation

**Step 4: Run verification**

Run:

```bash
go test ./pkg/websocket -run 'TestClientDial_.*' -count=1
go test -race ./pkg/websocket -count=1
go test -bench='Handshake|ValidateHandshakeResponse' -benchmem ./pkg/websocket
```

Expected:
- Regression tests remain green.
- Benchmark allocs decrease or CPU cost improves.

**Step 5: Commit**

```bash
git add pkg/websocket/client.go pkg/websocket/client_test.go pkg/websocket/benchmark_test.go
git commit -m "perf: reduce websocket client handshake allocations"
```

### Task 4: Make Benchmark And Integration Harnesses Deterministic

**Files:**
- Modify: `pkg/websocket/integration_test.go`
- Modify: `cmd/example/main_bench_test.go`
- Modify: `cmd/example/benchmark_comparison_test.go`

**Step 1: Write the failing reliability check**

Use shuffled and repeated runs as the failure detector:

```bash
go test -shuffle=on -count=10 ./pkg/websocket ./cmd/example
```

Expected today:
- This may still pass locally, but it is the command we will use to validate the harness cleanup.

**Step 2: Write minimal implementation**

Standardize helper patterns:

```go
func reserveLoopbackAddr(tb testing.TB) string { ... }
func waitForTCPServer(tb testing.TB, addr string) { ... }
func waitForHTTPServer(tb testing.TB, url string) { ... }
```

Replace:
- fixed ports
- large `time.Sleep(...)` startup waits

with:
- reserved ephemeral loopback addresses
- readiness polling
- channel-based open notifications where possible

**Step 3: Run verification**

Run:

```bash
go test ./pkg/websocket -run 'TestWebSocket(Integration|CustomHeadersAndCookies|Compression)$' -count=1
go test ./cmd/example -run '^$' -bench 'Benchmark(Hon|Std)$' -benchtime=1x
go test -shuffle=on -count=10 ./pkg/websocket ./cmd/example
```

Expected:
- Existing tests and benchmark smoke runs remain green.
- Startup/connection timing becomes deterministic.

**Step 4: Commit**

```bash
git add pkg/websocket/integration_test.go cmd/example/main_bench_test.go cmd/example/benchmark_comparison_test.go
git commit -m "test: stabilize performance harnesses"
```

### Task 5: Re-Evaluate The HTTP Pre-Scan Path

**Files:**
- Modify: `pkg/engine/engine.go`
- Modify: `pkg/engine/engine_test.go`
- Test: `pkg/engine/benchmark_test.go`

**Step 1: Write the failing performance hypothesis test**

Add targeted benchmarks for:
- `Peek + CheckRequest + http.ReadRequest`
- `http.ReadRequest` only on already-buffered complete requests

```go
func BenchmarkEngineServeHTTP_PreScan(b *testing.B) { ... }
func BenchmarkEngineServeHTTP_NoPreScan(b *testing.B) { ... }
```

**Step 2: Run the benchmark before implementation**

Run:

```bash
go test -bench='PreScan|NoPreScan|EngineServeHTTP' -benchmem ./pkg/engine
```

Expected:
- Evidence that the pre-scan dominates or does not dominate the HTTP path.

**Step 3: Write minimal implementation**

Only if the benchmark clearly supports it, narrow the pre-scan path. Example direction:

```go
if state.Reader.Buffered() == 0 {
    if r := conn.Reader(); r != nil && r.Len() > 0 {
        peekLen := min(r.Len(), parser.MaxHeaderSize+1)
        peekBuf, _ := r.Peek(peekLen)
        check := parser.CheckRequest(peekBuf)
        ...
    }
}
```

Do not remove the correctness guard entirely without a dedicated incomplete-request regression test.

**Step 4: Run verification**

Run:

```bash
go test ./pkg/engine -count=1
go test -race ./pkg/engine -count=1
go test -bench='PreScan|NoPreScan|EngineServeHTTP' -benchmem ./pkg/engine
```

Expected:
- Benchmarks justify the change.
- Incomplete request handling still behaves correctly.

**Step 5: Commit**

```bash
git add pkg/engine/engine.go pkg/engine/engine_test.go pkg/engine/benchmark_test.go
git commit -m "perf: narrow engine request pre-scan overhead"
```

### Task 6: Decide Whether WebSocket Write Path Needs A Structural Change

**Files:**
- Modify: `pkg/websocket/writer.go`
- Test: `pkg/websocket/benchmark_test.go`
- Reference: `pkg/websocket/websocket.go`

**Step 1: Write the benchmark first**

Add dedicated write-path benchmarks:

```go
func BenchmarkWriteMessage_ServerText(b *testing.B) { ... }
func BenchmarkWriteMessage_ClientMaskedText(b *testing.B) { ... }
```

**Step 2: Run benchmark before any structural change**

Run:

```bash
go test -bench='WriteMessage|ServeReactor' -benchmem ./pkg/websocket
```

Expected:
- Clear baseline for `net.Conn` fallback cost.

**Step 3: Decide implementation path**

Two options:

1. Keep `net.Conn` fallback if the measured cost is acceptable.
2. Prototype `netpoll.Writer` use behind tests if the fallback clearly dominates.

Recommended default:
- Do not change this path until the benchmark proves it is a meaningful bottleneck.

**Step 4: Verify**

If the path is left unchanged:
- document that benchmark evidence did not justify a risky structural change.

If changed:

```bash
go test ./pkg/websocket -count=1
go test -race ./pkg/websocket -count=1
go test -bench='WriteMessage|ServeReactor' -benchmem ./pkg/websocket
```

**Step 5: Commit**

```bash
git add pkg/websocket/writer.go pkg/websocket/benchmark_test.go
git commit -m "perf: benchmark websocket write-path tradeoffs"
```
