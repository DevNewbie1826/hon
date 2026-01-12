package websocket

import (
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
	pool512b = sync.Pool{New: func() any { return make([]byte, 512) }}
	pool4k   = sync.Pool{New: func() any { return make([]byte, 4096) }}
	pool16k  = sync.Pool{New: func() any { return make([]byte, 16*1024) }}
	pool64k  = sync.Pool{New: func() any { return make([]byte, 64*1024) }}
)

// getPayloadBuffer returns a byte slice of at least 'size' capacity.
func getPayloadBuffer(size int) []byte {
	if size <= 512 {
		return pool512b.Get().([]byte)[:size]
	}
	if size <= 4096 {
		return pool4k.Get().([]byte)[:size]
	}
	if size <= 16*1024 {
		return pool16k.Get().([]byte)[:size]
	}
	if size <= 64*1024 {
		return pool64k.Get().([]byte)[:size]
	}
	// Direct allocation for very large messages to avoid long-term retention
	return make([]byte, size)
}

// putPayloadBuffer returns the buffer to the appropriate pool.
func putPayloadBuffer(buf []byte) {
	c := cap(buf)
	switch c {
	case 512:
		pool512b.Put(buf)
	case 4096:
		pool4k.Put(buf)
	case 16 * 1024:
		pool16k.Put(buf)
	case 64 * 1024:
		pool64k.Put(buf)
	default:
		// Do nothing for buffers that don't match our bucket sizes (let GC collect them)
	}
}
