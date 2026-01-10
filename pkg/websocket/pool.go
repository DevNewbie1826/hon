package websocket

import (
	"math/bits"
	"sync"
)

// Bucketed Pool Strategy
// We use multiple pools for different size ranges to minimize memory waste and GC pressure.
//
// Sizes:
//  0:  0 - 512 B    (Control frames, small JSON)
//  1:  513 - 4 KB   (Standard JSON messages)
//  2:  4 KB - 16 KB (Medium payloads)
//  3:  16 KB - 64 KB (Larger payloads)
//  Everything > 64KB is allocated directly to avoid holding large chunks in memory.

var (
	pool512b = sync.Pool{New: func() any { b := make([]byte, 512); return &b }}
	pool4k   = sync.Pool{New: func() any { b := make([]byte, 4096); return &b }}
	pool16k  = sync.Pool{New: func() any { b := make([]byte, 16*1024); return &b }}
	pool64k  = sync.Pool{New: func() any { b := make([]byte, 64*1024); return &b }}
)

// getPayloadBuffer returns a byte slice of at least 'size' capacity.
func getPayloadBuffer(size int) []byte {
	if size <= 512 {
		bufPtr := pool512b.Get().(*[]byte)
		return (*bufPtr)[:size]
	}
	if size <= 4096 {
		bufPtr := pool4k.Get().(*[]byte)
		return (*bufPtr)[:size]
	}
	if size <= 16*1024 {
		bufPtr := pool16k.Get().(*[]byte)
		return (*bufPtr)[:size]
	}
	if size <= 64*1024 {
		bufPtr := pool64k.Get().(*[]byte)
		return (*bufPtr)[:size]
	}
	// Direct allocation for very large messages to avoid long-term retention
	return make([]byte, size)
}

// putPayloadBuffer returns the buffer to the appropriate pool.
func putPayloadBuffer(buf []byte) {
	c := cap(buf)
	if c <= 512 {
		b := buf[:512]
		pool512b.Put(&b)
		return
	}
	if c <= 4096 {
		b := buf[:4096]
		pool4k.Put(&b)
		return
	}
	if c <= 16*1024 {
		b := buf[:16*1024]
		pool16k.Put(&b)
		return
	}
	if c <= 64*1024 {
		b := buf[:64*1024]
		pool64k.Put(&b)
		return
	}
	// Do nothing for larger buffers (let GC collect them)
}

// NextPowerOf2 is a helper (if needed in future)
func nextPowerOf2(n int) int {
	if n <= 0 {
		return 1
	}
	return 1 << (32 - bits.LeadingZeros32(uint32(n-1)))
}
