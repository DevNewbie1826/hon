package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	hengine "github.com/DevNewbie1826/hon/pkg/engine"
	hserver "github.com/DevNewbie1826/hon/pkg/server"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/adaptor"
	"github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/lxzan/gws"
)

func main() {
	serverType := flag.String("type", "hon", "Server type: hon, hertz, std")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/", rootHandler)
	mux.HandleFunc("/file", fileHandler) // 파일 서빙 핸들러 등록
	mux.HandleFunc("/sse", sseHandler)
	mux.HandleFunc("/ws", func(writer http.ResponseWriter, request *http.Request) {
		socket, err := upgrader.Upgrade(writer, request)
		if err != nil {
			log.Printf("WebSocket upgrade failed: %v", err)
			return
		}
		go func() {
			socket.ReadLoop()
		}()
	})

	addr := ":1826"

	if *serverType == "hertz" {
		hertz(mux, addr)
		return
	}

	if *serverType == "std" {
		std(mux, addr)
	}

	hon(mux, addr)
}

func hertz(mux http.Handler, addr string) {
	hlog.SetLevel(hlog.LevelFatal)
	// 2. Hertz 서버 생성
	hertzServer := server.Default(
		server.WithHostPorts(addr),
		server.WithSenseClientDisconnection(true),
	)
	//hertzServer.NoHijackConnPool = true

	// 3. Hertz 어댑터를 사용하여 Gin 엔진을 Hertz 핸들러로 래핑
	hertzServer.Any("/*path", adaptor.HertzHandler(mux))

	log.Printf("Hertz server running http.mux application on %s", addr)

	// 4. Hertz 서버 시작
	hertzServer.Spin()
}

func std(mux http.Handler, addr string) {
	log.Printf("Standard net/http server starting on %s...", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Standard server failed: %v", err)
	}
}

func hon(mux http.Handler, addr string) {
	eng := hengine.NewEngine(mux)

	srv := hserver.NewServer(eng,
		hserver.WithReadTimeout(10*time.Second),
		hserver.WithWriteTimeout(10*time.Second),
	)

	log.Printf("Starting Hon server on %s", addr)
	if err := srv.Serve(addr); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// rootHandler는 일반 HTTP 요청을 처리합니다.
func rootHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "Welcome! Try /sse endpoint.")
}

// sseHandler는 Server-Sent Events를 스트리밍합니다.
func sseHandler(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			log.Println("Client disconnected from SSE")
			return
		case t := <-ticker.C:
			// SSE 이벤트 데이터는 "data: 메시지\r\n\r\n" 형식으로 보냅니다.
			fmt.Fprintf(w, "data: Server time is %v\r\n\r\n", t)
			flusher.Flush()
		}
	}
}

// fileHandler는 파일을 서빙하며 sendfile을 활용합니다.
func fileHandler(w http.ResponseWriter, r *http.Request) {
	// 예시로 현재 실행 중인 main.go 파일을 서빙합니다.
	// 실제로는 대용량 비디오나 이미지를 서빙할 때 유용합니다.
	http.ServeFile(w, r, "README.md")
}

var upgrader = gws.NewUpgrader(&Handler{}, &gws.ServerOption{
	ParallelEnabled:   true,                                 // 병렬 메시지 처리
	Recovery:          gws.Recovery,                         // 패닉 복구
	PermessageDeflate: gws.PermessageDeflate{Enabled: true}, // 압축 활성화
})

const (
	PingInterval = 5 * time.Second
	PingWait     = 10 * time.Second
)

type Handler struct{}

func (c *Handler) OnOpen(socket *gws.Conn) {
	_ = socket.SetDeadline(time.Now().Add(PingInterval + PingWait))
}

func (c *Handler) OnClose(socket *gws.Conn, err error) {}

func (c *Handler) OnPing(socket *gws.Conn, payload []byte) {
	_ = socket.SetDeadline(time.Now().Add(PingInterval + PingWait))
	_ = socket.WritePong(nil)
}

func (c *Handler) OnPong(socket *gws.Conn, payload []byte) {}

func (c *Handler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer message.Close()
	socket.WriteMessage(message.Opcode, message.Bytes())
}
