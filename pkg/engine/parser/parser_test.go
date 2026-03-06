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

func TestCheckRequest_ChunkedWithMultipleTrailersConsumesAll(t *testing.T) {
	data := "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nHello\r\n0\r\nFoo: bar\r\nBaz: qux\r\n\r\n"

	res := CheckRequest([]byte(data))
	if !res.Complete {
		t.Fatalf("expected request to be complete")
	}
	if res.Error != nil {
		t.Fatalf("expected no error, got %v", res.Error)
	}
	if res.BytesConsumed != len(data) {
		t.Fatalf("expected bytes consumed %d, got %d", len(data), res.BytesConsumed)
	}
}

func TestCheckRequest_InvalidContentLengthReturnsError(t *testing.T) {
	res := CheckRequest([]byte("POST / HTTP/1.1\r\nContent-Length: abc\r\n\r\n"))
	if res.Error == nil {
		t.Fatalf("expected invalid content-length to return an error")
	}
	if res.Complete {
		t.Fatalf("expected invalid content-length request to be incomplete")
	}
}

func TestCheckRequest_NegativeContentLengthReturnsError(t *testing.T) {
	res := CheckRequest([]byte("POST / HTTP/1.1\r\nContent-Length: -1\r\n\r\n"))
	if res.Error == nil {
		t.Fatalf("expected negative content-length to return an error")
	}
	if res.Complete {
		t.Fatalf("expected negative content-length request to be incomplete")
	}
}

func TestCheckRequest_ChunkedHugeSizeNoPanicReturnsError(t *testing.T) {
	data := []byte("POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n7FFFFFFFFFFFFFFF\r\nX\r\n")

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("CheckRequest panicked: %v", r)
		}
	}()

	res := CheckRequest(data)
	if res.Error == nil {
		t.Fatalf("expected huge chunk size to return an error")
	}
	if res.Complete {
		t.Fatalf("expected huge chunk size request to be incomplete")
	}
}

func TestCheckRequest_HugeContentLengthReturnsError(t *testing.T) {
	res := CheckRequest([]byte("POST / HTTP/1.1\r\nContent-Length: 999999999999999999999999\r\n\r\n"))
	if res.Error == nil {
		t.Fatalf("expected huge content-length to return an error")
	}
	if res.Complete {
		t.Fatalf("expected huge content-length request to be incomplete")
	}
}
