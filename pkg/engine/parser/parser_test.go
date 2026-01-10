package parser

import (
	"testing"
)

func TestCheckRequest(t *testing.T) {
	tests := []struct {
		name          string
		data          string
		expectedComp  bool
		expectedBytes int
	}{
		{
			name:         "Incomplete Header",
			data:         "GET / HTTP/1.1\r\nHost: local",
			expectedComp: false,
		},
		{
			name:          "GET No Body Complete",
			data:          "GET / HTTP/1.1\r\nHost: localhost\r\n\r\n",
			expectedComp:  true,
			expectedBytes: len("GET / HTTP/1.1\r\nHost: localhost\r\n\r\n"),
		},
		{
			name:          "POST with Content-Length Complete",
			data:          "POST / HTTP/1.1\r\nContent-Length: 5\r\n\r\nHello",
			expectedComp:  true,
			expectedBytes: len("POST / HTTP/1.1\r\nContent-Length: 5\r\n\r\nHello"),
		},
		{
			name:         "POST with Content-Length Incomplete",
			data:         "POST / HTTP/1.1\r\nContent-Length: 10\r\n\r\nHello",
			expectedComp: false,
		},
		{
			name:          "Chunked Encoding Complete",
			data:          "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nHello\r\n0\r\n\r\n",
			expectedComp:  true,
			expectedBytes: len("POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nHello\r\n0\r\n\r\n"),
		},
		{
			name:         "Chunked Encoding Incomplete",
			data:         "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nHello\r\n",
			expectedComp: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := CheckRequest([]byte(tt.data))
			if res.Complete != tt.expectedComp {
				t.Errorf("expected complete %v, got %v", tt.expectedComp, res.Complete)
			}
			if tt.expectedComp && res.BytesConsumed != tt.expectedBytes {
				t.Errorf("expected bytes %d, got %d", tt.expectedBytes, res.BytesConsumed)
			}
		})
	}
}
