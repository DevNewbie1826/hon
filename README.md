# Hon (혼)

**Hon**은 [CloudWeGo Netpoll](https://github.com/cloudwego/netpoll) 기반의 고성능 HTTP 엔진 어댑터입니다. **Hon**은 "HTTP-over-Netpoll"의 약자입니다.

Go 언어의 표준 `net/http` 인터페이스를 그대로 사용하면서, 이벤트 기반(epoll/kqueue)의 Reactor 패턴을 통한 고성능 I/O 처리를 가능하게 합니다. 이를 통해 Gin, Chi, Echo 등 기존의 인기 있는 Go 웹 프레임워크를 코드 변경 없이 Netpoll 위에서 실행하며, 대규모 동시 접속 환경을 효율적으로 관리할 수 있습니다.

## 🚀 주요 특징 (Key Features)

- **압도적인 리소스 효율성**: Reactor 패턴을 통해 **10,000개 이상의 동시 접속자**가 있어도 단 **수십 개의 고루틴**만으로 서버를 운영할 수 있습니다. (표준 서버 대비 고루틴 사용량 99% 절감)
- **표준 호환성**: `http.Handler` 인터페이스를 완벽하게 지원하여 `chi`, `gin`, `echo`, `mux` 등 기존 라우터를 그대로 사용 가능합니다.
- **메모리 최적화**: `WithBufferSize` 옵션을 통해 연결당 메모리 점유율을 튜닝할 수 있으며, 최적화 시 **연결당 약 20KB** 수준의 낮은 메모리 사용량을 보장합니다.
- **이벤트 기반 WebSocket (Reactor Mode)**: `SetReadHandler`를 통해 WebSocket 연결조차 고루틴 점유 없이 이벤트 루프에서 처리하는 독보적인 성능을 제공합니다.
- **SSE 지원**: `http.Flusher` 구현을 통해 효율적인 Server-Sent Events 스트리밍을 지원합니다.
- **SO_REUSEPORT 지원**: 멀티 프로세스/스레드 환경에서의 수평적 성능 확장을 지원합니다.

## 📊 성능 지표 (Performance Snapshot)

10,000개 동시 WebSocket 접속 시(Mac OS 환경):

| 지표 | 표준 서버 (net/http) | **Hon (Netpoll)** | 개선 효과 |
| :--- | :--- | :--- | :--- |
| **고루틴 개수** | 20,000+ 개 | **6 개** | **99.9% 절감** |
| **메모리 사용량** | 340+ MB | **203 MB** | **약 40% 절약** |

## 📦 설치 (Installation)

```bash
go get github.com/DevNewbie1826/hon
```

## 💡 사용 예제 (Usage)

### 기본 사용법 (최적화 옵션 포함)

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

	// 1KB 버퍼 설정을 통해 메모리 효율 극대화 (Default: 4KB)
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

### 이벤트 기반 WebSocket 처리 (Reactor Mode)

고루틴을 생성하지 않고 WebSocket 메시지를 처리하는 가장 효율적인 방법입니다.

```go
func wsHandler(w http.ResponseWriter, r *http.Request) {
    // Upgrade connection...
    if hijacker, ok := w.(adaptor.Hijacker); ok {
        hijacker.SetReadHandler(func(c net.Conn, rw *bufio.ReadWriter) error {
            // 이 콜백은 데이터가 도착했을 때만 Netpoll 워커에 의해 실행됩니다.
            // 무한 루프를 돌릴 필요가 없습니다.
            msg, _ := wsutil.ReadClientData(rw)
            wsutil.WriteServerMessage(rw, ws.OpText, msg)
            rw.Flush()
            return nil
        })
    }
}
```

## 🛠 성능 테스트 (Stress Test)

프로젝트 루트에 포함된 `ws_stress_config.go`를 사용하여 직접 성능을 검증할 수 있습니다.

```bash
# 1만개 연결을 30초간 유지하며 테스트
go run ws_stress_config.go -c 10000 -hold 30s
```

## 🏗 아키텍처 (Architecture)

- **Server**: Netpoll의 EventLoop를 관리하고 TCP 연결을 수신합니다.
- **Engine**: Connection별 상태(`ConnectionState`) 및 버퍼 풀을 관리하며, 핸들러로 요청을 디스패치합니다.
- **Adaptor**: Netpoll의 raw connection과 표준 `net/http` 객체 간의 변환을 담당합니다.

## 🤝 기여 (Contributing)

버그 리포트나 기능 제안은 언제나 환영합니다. 이슈를 등록하거나 PR을 보내주세요.

## 📄 라이선스 (License)

MIT License