package parser

import (
	"testing"
)

func BenchmarkCheckRequest_Chunked(b *testing.B) {
	data := []byte("POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nHello\r\n0\r\n\r\n")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CheckRequest(data)
	}
}

func BenchmarkCheckRequest_ContentLength(b *testing.B) {
	data := []byte("POST / HTTP/1.1\r\nContent-Length: 5\r\n\r\nHello")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CheckRequest(data)
	}
}
