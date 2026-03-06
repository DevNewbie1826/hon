# Performance Optimization Design

## Goal

현재 `Hon`의 HTTP/WS hot path에서 확인된 성능 리스크를 위험도별로 나누고, 측정 기반으로 개선 순서를 정한다.

## Success Criteria

- HTTP 경로에서 불필요한 스케줄러 양보와 중복 스캔 비용을 줄인다.
- WebSocket handshake / write path의 불필요한 할당과 fallback 비용을 줄인다.
- 성능 변경은 `go test`, `go test -race`, `go test -bench` 기준으로 회귀 없이 검증한다.

## Approaches

### Approach 1: Low-Risk Benchmark-First

- 먼저 기존 벤치를 세분화하고, `runtime.Gosched`, handshake parser, `ReadFrom` fast path 같은 병목 후보를 실험적으로 줄인다.
- 장점: correctness 리스크가 가장 낮고, 어떤 가설이 실제 병목인지 빠르게 확인할 수 있다.
- 단점: 큰 구조 개선 없이 얻는 이득이 제한될 수 있다.

### Approach 2: Mid-Risk Hot-Path Simplification

- HTTP 요청 경로에서 `Peek + CheckRequest + http.ReadRequest`의 중복 작업을 줄이고, WebSocket write path를 `netpoll.Writer` 쪽으로 되돌리는 구조적 단순화를 시도한다.
- 장점: throughput 개선 여지가 크다.
- 단점: incomplete request 처리, reactor write semantics 같은 correctness 리스크가 생긴다.

### Approach 3: High-Risk Parser/Adaptor Rewrite

- `http.ReadRequest` 의존을 줄이고, 요청/응답 경로를 더 직접적인 zero-copy/zero-alloc 흐름으로 재구성한다.
- 장점: 장기적으로 가장 큰 개선 여지가 있다.
- 단점: 범위가 커지고, 현 시점 리뷰의 목적을 넘어선다.

## Recommendation

추천은 `Approach 1 -> 일부 Approach 2` 순서다.

- 1단계: 측정과 회귀 테스트를 더 촘촘히 만든다.
- 2단계: `runtime.Gosched` 제거, handshake parser 할당 축소, flaky benchmark/integration 보강 같이 저위험 변경을 먼저 적용한다.
- 3단계: 결과가 충분하지 않을 때만 `Peek + CheckRequest` 중복 경로와 `netpoll.Writer` 복귀를 실험 브랜치로 진행한다.

## Work Buckets

### Low-Risk Experiments

- `serveHTTP`의 unconditional `runtime.Gosched()` 제거 효과 측정
- WebSocket handshake parser의 `string/split` 할당 축소
- integration/benchmark helper를 통해 readiness polling 표준화

### Medium-Risk Improvements

- HTTP pre-scan 경로(`Peek + CheckRequest`)를 조건부/축소 적용
- `ResponseWriter.ReadFrom` fast path가 실제로 열리도록 API/헤더 결정 시점 재검토
- WebSocket write path의 `net.Conn` fallback 비용 재평가

### Structural Reconsideration

- `http.ReadRequest` 유지 vs custom parser/adaptor 심화
- server/client에서 zero-copy 경계와 safety contract 재정의

## Validation Strategy

- 기능 검증: `go test ./...`, `go test -race ./...`
- 정적 검증: `go vet ./...`, `staticcheck ./...`
- 성능 검증:
  - `go test -bench=. -benchmem ./pkg/engine`
  - `go test -bench=. -benchmem ./pkg/websocket`
  - `go test -bench=. -benchmem ./cmd/example`
  - 필요 시 before/after 결과를 별도 메모로 남긴다.
