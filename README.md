# Hon (혼)

**Hon**은 [CloudWeGo Netpoll](https://github.com/cloudwego/netpoll) 기반의 고성능 HTTP 엔진 어댑터입니다.

Go 언어의 표준 `net/http` 인터페이스를 그대로 사용하면서, 이벤트 기반(epoll/kqueue)의 고성능 I/O 처리를 가능하게 합니다. 이를 통해 Gin, Chi, Echo 등 기존의 인기 있는 Go 웹 프레임워크를 코드 변경 없이 Netpoll 위에서 실행할 수 있습니다.

## 🚀 주요 특징 (Key Features)

- **표준 호환성**: `http.Handler` 인터페이스를 완벽하게 지원하여 `chi`, `gin`, `echo`, `mux` 등 기존 라우터를 그대로 사용 가능합니다.
- **고성능 I/O**: Netpoll을 사용하여 대규모 동시 접속(C10K+) 환경에서도 효율적인 I/O 처리를 제공합니다.
- **Zero-Copy 최적화**: 내부적으로 버퍼 풀(`bytebufferpool`)을 사용하여 메모리 할당을 최소화합니다.
- **SSE 및 WebSocket 지원**: `http.Flusher` 구현을 통한 Server-Sent Events(SSE) 지원 및 `Hijack`을 통한 WebSocket 업그레이드를 지원합니다.
- **SO_REUSEPORT 지원**: 멀티 프로세스/스레드 환경에서의 성능 확장을 위해 포트 재사용을 기본으로 지원합니다.

## 📦 설치 (Installation)

```bash
go get github.com/DevNewbie1826/hon
```

## 💡 사용 예제 (Usage)

Hon은 표준 `http.Handler`를 감싸서 실행하는 방식으로 동작합니다. 아래는 표준 `http.ServeMux`를 사용하는 예제입니다.

### 기본 사용법

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
	// 1. 기존 핸들러 생성 (예: http.ServeMux, chi.NewRouter, gin.Default 등)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Hello, Hon!"))
	})

	// 2. Hon 엔진 생성
	eng := engine.NewEngine(mux, engine.WithRequestTimeout(5*time.Second))

	// 3. 서버 설정 및 시작
	srv := server.NewServer(eng,
		server.WithReadTimeout(10*time.Second),
		server.WithWriteTimeout(10*time.Second),
	)

	log.Println("Server listening on :8080")
	if err := srv.Serve(":8080"); err != nil {
		log.Fatal(err)
	}
}
```

### Gin 프레임워크와 함께 사용하기

```go
package main

import (
	"github.com/gin-gonic/gin"
	"github.com/DevNewbie1826/hon/pkg/engine"
	"github.com/DevNewbie1826/hon/pkg/server"
)

func main() {
	r := gin.New()
	r.GET("/ping", func(c *gin.Context) {
		c.String(200, "pong")
	})

	eng := engine.NewEngine(r)
	srv := server.NewServer(eng)
	srv.Serve(":8080")
}
```

### SSE (Server-Sent Events) 지원

Hon은 `http.Flusher` 인터페이스를 구현하고 있어 SSE 스트리밍이 가능합니다.

```go
func sseHandler(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", 500)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	for {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(1 * time.Second):
			fmt.Fprintf(w, "data: %s\n\n", time.Now().String())
			flusher.Flush()
		}
	}
}
```

## 🏗 아키텍처 (Architecture)

- **Server**: Netpoll의 EventLoop를 관리하고 TCP 연결을 수신합니다.
- **Engine**: 연결된 Connection에서 HTTP 요청을 파싱하고 사용자의 `http.Handler`로 전달합니다.
- **Adaptor**: Netpoll의 raw connection과 표준 `net/http` 객체(`Request`, `ResponseWriter`) 간의 변환을 담당합니다.

## 🤝 기여 (Contributing)

버그 리포트나 기능 제안은 언제나 환영합니다. 이슈를 등록하거나 PR을 보내주세요.

## 📄 라이선스 (License)

MIT License
