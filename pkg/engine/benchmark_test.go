package engine

import (
	"net/http"
	"testing"
	"time"
)

func BenchmarkEngineServeHTTP_SmallKeepAlive(b *testing.B) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	eng := NewEngine(handler)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		conn := &MockConnection{}
		conn.fillRequest("GET", "/", "")
		state := NewConnectionState(time.Second)
		b.StartTimer()

		if err := eng.ServeConn(state, conn); err != nil {
			b.Fatalf("ServeConn failed: %v", err)
		}

		b.StopTimer()
		eng.ReleaseConnectionState(state)
	}
}

func BenchmarkEngineServeHTTP_SmallKeepAliveWithRequestTimeout(b *testing.B) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	eng := NewEngine(handler, WithRequestTimeout(time.Second))

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		conn := &MockConnection{}
		conn.fillRequest("GET", "/", "")
		state := NewConnectionState(time.Second)
		b.StartTimer()

		if err := eng.ServeConn(state, conn); err != nil {
			b.Fatalf("ServeConn failed: %v", err)
		}

		b.StopTimer()
		eng.ReleaseConnectionState(state)
	}
}

func BenchmarkEngineServeHTTP_Pipelined(b *testing.B) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	eng := NewEngine(handler)
	data := []byte(
		"GET /?id=1 HTTP/1.1\r\nHost: localhost\r\n\r\n" +
			"GET /?id=2 HTTP/1.1\r\nHost: localhost\r\n\r\n",
	)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		conn := &MockConnection{}
		conn.readBuf.Write(data)
		conn.reader = newMockNetpollReader(data)
		state := NewConnectionState(time.Second)
		b.StartTimer()

		if err := eng.ServeConn(state, conn); err != nil {
			b.Fatalf("ServeConn failed: %v", err)
		}

		b.StopTimer()
		eng.ReleaseConnectionState(state)
	}
}

func BenchmarkEngineServeHTTP_PreScan(b *testing.B) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	eng := NewEngine(handler)
	reqBytes := []byte("GET / HTTP/1.1\r\nHost: localhost\r\n\r\n")

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		conn := &MockConnection{}
		conn.readBuf.Write(reqBytes)
		conn.reader = newMockNetpollReader(reqBytes)
		state := NewConnectionState(time.Second)
		b.StartTimer()

		if err := eng.ServeConn(state, conn); err != nil {
			b.Fatalf("ServeConn failed: %v", err)
		}

		b.StopTimer()
		eng.ReleaseConnectionState(state)
	}
}

func BenchmarkEngineServeHTTP_NoPreScan(b *testing.B) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	eng := NewEngine(handler)
	reqBytes := []byte("GET / HTTP/1.1\r\nHost: localhost\r\n\r\n")

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		conn := &MockConnection{}
		conn.readBuf.Write(reqBytes)
		state := NewConnectionState(time.Second)
		b.StartTimer()

		if err := eng.ServeConn(state, conn); err != nil {
			b.Fatalf("ServeConn failed: %v", err)
		}

		b.StopTimer()
		eng.ReleaseConnectionState(state)
	}
}
