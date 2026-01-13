# Hon (혼)

[🇰🇷 한국어 (Korean)](README_KO.md) | [🇺🇸 English](README.md)

**Hon**은 [CloudWeGo Netpoll](https://github.com/cloudwego/netpoll) 기반의 고성능 HTTP 엔진 어댑터입니다. 표준 Go 웹 프레임워크(Gin, Chi, Echo 등)가 코드 변경 없이 **Reactor 패턴**을 활용하여 압도적인 동시성 처리 능력을 갖추도록 해줍니다.

---

## 🚀 왜 Hon인가요?

기존 Go 서버(`net/http`)는 '연결 당 하나의 고루틴(Thread-per-Connection)'을 생성합니다. Go의 스케줄러가 아무리 뛰어나도, 10만 개 이상의 연결을 유지하면 GB 단위의 메모리를 소모하고 스케줄러 부하가 발생합니다.

**Hon**은 다릅니다:
- **Reactor 패턴**: 비동기 I/O와 고루틴 풀(`gopool`)을 활용하여 효율적으로 연결을 처리합니다.
- **Zero-Allocation**: 불필요한 메모리 할당을 제거하여 GC 부하를 최소화했습니다.
- **Zero-Code Integration**: 기존 `http.ListenAndServe`를 대체하기만 하면 즉시 적용됩니다.

### ⚡ 주요 특징
- **압도적인 동시성**: 벤치마크에서 단 **6개의 고루틴**으로 **25,000개 이상의 연결**을 안정적으로 처리.
- **리소스 효율성**: 고루틴 오버헤드를 제거하여 CPU와 메모리 사용 효율 극대화.
- **진정한 Reactor 클라이언트**: 서버와 동일한 Reactor 아키텍처를 사용하여 **연결당 0개의 고루틴**으로 동작하는 WebSocket 클라이언트 제공.
- **표준 호환성**: `http.Handler` 완전 호환. `chi`, `gin`, `echo`, `mux` 등 기존 라우터를 그대로 사용하세요.
- **안전성(Safety)**: DoS 방어(헤더 크기 제한) 및 엔진 레벨의 Panic 복구 메커니즘 내장.

---

## 📦 시작하기 (Quick Start)

### 1. 설치
```bash
go get github.com/DevNewbie1826/hon
```

### 2. 기본 사용법 (코드 변경 없음)
기존 핸들러를 `hon` 엔진으로 감싸기만 하면 됩니다.

```go
package main

import (
    "net/http"
    "github.com/DevNewbie1826/hon/pkg/engine"
    "github.com/DevNewbie1826/hon/pkg/server"
)

func main() {
    // 1. 표준 핸들러 생성 (Gin, Chi, Mux 등 무엇이든 가능)
    mux := http.NewServeMux()
    mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        w.Write([]byte("Hello, Hon!"))
    })

    // 2. Hon 엔진으로 래핑
    eng := engine.NewEngine(mux)

    // 3. Hon 서버로 실행 (http.ListenAndServe 대신 사용)
    server.NewServer(eng).Serve(":8080")
}
```

---

## 🛡️ 베스트 프랙티스 & 안전 가이드

Hon의 성능을 100% 활용하고 안전하게 운영하기 위해 아래 가이드를 준수해 주세요.

### ✅ 권장 사항 (DO)

1.  **Context 적극 활용**:
    Hon은 연결 상태(`ConnectionState`)를 `context.Context`로 효율적(Zero-Alloc)으로 구현했습니다. `r.Context()`를 사용하여 타임아웃과 취소(Cancellation)를 올바르게 처리하세요.
    ```go
    select {
    case <-r.Context().Done():
        return // 연결 종료 시 즉시 중단
    case <-time.After(time.Second):
        // 작업 수행
    }
    ```

2.  **WebSocket 어댑터 사용**:
    WebSocket 구현 시 `hon/pkg/adaptor`를 사용하면 Reactor 루프와 완벽하게 통합된 고성능 통신이 가능합니다.

### 🛑 주의 사항 (DON'T) - 중요!

1.  **핸들러에서 긴 블로킹 작업 금지**:
    Hon은 내부적으로 **고루틴 풀(`gopool`)**을 사용하여 고루틴을 재사용합니다. 
    *   핸들러가 블로킹되면(긴 대기 시간, 무거운 작업 등), 해당 워커는 재사용될 수 없으므로 새로운 요청을 처리하기 위해 **새로운 고루틴이 계속 생성**됩니다.
    *   이는 Reactor 패턴의 장점을 없애고, 사실상 표준 서버처럼 동작하게 만들어 **고루틴 폭발(Goroutine Explosion)**을 유발합니다.
    *   **해결책**: 시간이 오래 걸리는 작업은 별도의 고루틴(`go func()`)으로 분리하거나 비동기 API를 사용하세요.

2.  **WebSocket `payload` 무단 저장(Retain) 금지**:
    > [!WARNING]
    > **CRITICAL SAFETY NOTICE**
    
    `OnMessage` 콜백으로 전달되는 `payload` 바이트 슬라이스는 네트워크 내부 버퍼를 직접 가리키는 **Zero-Copy 슬라이스**입니다.
    *   이 데이터는 `OnMessage` 함수가 실행되는 동안에만 **유효**합니다.
    *   만약 데이터를 **비동기적으로 사용**하거나(다른 고루틴 전달), **저장**해야 한다면 반드시 **복사(Copy)**해야 합니다.

    ```go
    func (h *MyHandler) OnMessage(c net.Conn, op ws.OpCode, payload []byte, fin bool) {
        // ✅ OK: 동기적인 처리 (로그 출력, 파싱 등)
        fmt.Println(string(payload)) 
        
        // ❌ BAD: 포인터 직접 저장 (데이터 오염 위험!)
        // globalStore = payload 

        // ✅ OK: 비동기 처리나 저장을 위해 복사
        dataCopy := make([]byte, len(payload))
        copy(dataCopy, payload)
        go processAsync(dataCopy)
    }
    ```

---

## 📊 성능 벤치마크

macOS M4 (Local) 환경 테스트 결과.

### 1. HTTP 처리량 (RPS)
| 모드 | 초당 요청 수 (RPS) | 성능 향상 |
| :--- | :--- | :--- |
| 표준 (`net/http`) | 129,370 | - |
| **Hon** | **237,856** | **+83.8%** |

### 2. WebSocket 효율성 (5,000개 연결)
| 모드 | 고루틴 수 | 메모리/리소스 효율 |
| :--- | :--- | :--- |
| 표준 (`gorilla`) | 5,006 | 100% (높음) |
| **Hon 서버** | **7** | **0.1% (극도로 효율적)** |

---

## 📄 라이선스

MIT License
