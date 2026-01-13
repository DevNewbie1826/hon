package parser_test

import (
	"bytes"
	"testing"

	"github.com/DevNewbie1826/hon/pkg/engine/parser"
)

func TestCheckRequest_DoS_LargeHeader(t *testing.T) {
	// Create a 1MB payload without header end (\r\n\r\n)
	// Just a continuous stream of garbage
	size := 1024 * 1024 // 1MB
	data := bytes.Repeat([]byte("X"), size)

	// Currently, this will scan the entire 1MB looking for \r\n\r\n.
	// In a real attack, this could be GBs.
	// We want the parser to fail FAST if it scanned more than, say, 8KB without finding the header end.

	// Since we haven't implemented the limit yet, we expect this function to behave "normally"
	// (i.e. return Incomplete) but we want to assert that it DOES NOT error properly (yet).
	// Once we fix it, this test should be updated to expect an Error or fail if it scans too much.

	// For reproduction, we can just verify that it returns Complete=false and BytesConsumed=0
	// But to prove the "DoS", we'd measure time, but for unit test, let's just assert correctness.

	res := parser.CheckRequest(data)
	if res.Error == nil {
		t.Errorf("Expected DoS protection error, got nil")
	}
	if res.Complete {
		t.Errorf("Should not be complete")
	}

	// FIX GOAL: When we implement the fix, CheckRequest should probably return an error or
	// complete=false with a specific indication that it gave up.
	// Actually, CheckRequest structure has an 'Error' field. Use it.
}

func TestCheckRequest_DoS_PartialHeader_Limit(t *testing.T) {
	// This test is designed to FAIL after the fix is implemented (regression test style),
	// or pass now.
	// We want to verify that currently it happily accepts a 10KB partial header.

	data := bytes.Repeat([]byte("Header-Key: Value\r\n"), 1000) // ~17KB
	res := parser.CheckRequest(data)

	if res.Error == nil {
		t.Fatalf("Expected DoS protection error, got nil")
	}
	if res.Complete {
		t.Errorf("Should not be complete as there is no \\r\\n\\r\\n")
	}
}
