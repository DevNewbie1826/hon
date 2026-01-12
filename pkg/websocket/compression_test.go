package websocket

import (
	"bytes"
	"testing"
)

func TestCompressDecompress(t *testing.T) {
	payload := []byte("Hello Compression World! " + string(bytes.Repeat([]byte("A"), 1000)))

	// 1. Compress
	compressed, err := CompressData(payload)
	if err != nil {
		t.Fatalf("Compress failed: %v", err)
	}

	t.Logf("Original size: %d, Compressed size: %d", len(payload), len(compressed))

	// 2. Decompress
	decompressed, err := DecompressData(compressed, 0) // 0 = unlimited
	if err != nil {
		t.Fatalf("Decompress failed: %v", err)
	}

	if !bytes.Equal(payload, decompressed) {
		t.Fatal("Payload mismatch after roundtrip")
	}
}
