package websocket

import (
	"bytes"
	"io"
	"sync"

	"github.com/klauspost/compress/flate"
)

var (
	flateReaderPool sync.Pool
	flateWriterPool sync.Pool
)

func getFlateReader(r io.Reader) io.ReadCloser {
	if v := flateReaderPool.Get(); v != nil {
		fr := v.(io.ReadCloser)
		if resetter, ok := fr.(flate.Resetter); ok {
			_ = resetter.Reset(r, nil)
		}
		return fr
	}
	return flate.NewReader(r)
}

func putFlateReader(fr io.ReadCloser) {
	if fr != nil {
		_ = fr.Close()
		flateReaderPool.Put(fr)
	}
}

func getFlateWriter(w io.Writer) *flate.Writer {
	if v := flateWriterPool.Get(); v != nil {
		fw := v.(*flate.Writer)
		fw.Reset(w)
		return fw
	}
	fw, _ := flate.NewWriter(w, flate.BestSpeed)
	return fw
}

func putFlateWriter(fw *flate.Writer) {
	if fw != nil {
		flateWriterPool.Put(fw)
	}
}

// CompressData compresses the payload using deflate.
// The returned slice is allocated from the pool. Caller MUST call putPayloadBuffer.
func CompressData(payload []byte) ([]byte, error) {
	// Initialize with a guess (e.g., half size or same size)
	// Usually compression reduces size, but for small messages it might increase slightly.
	initialCap := len(payload)
	if initialCap < 512 {
		initialCap = 512
	}

	pw := &PooledWriter{
		Buf: getPayloadBuffer(initialCap)[:0],
	}

	fw := getFlateWriter(pw)
	// Ensure writer is returned to pool even if Close fails
	defer putFlateWriter(fw)

	if _, err := fw.Write(payload); err != nil {
		putPayloadBuffer(pw.Buf)
		return nil, err
	}

	if err := fw.Close(); err != nil {
		putPayloadBuffer(pw.Buf)
		return nil, err
	}

	return pw.Buf, nil
}

// DecompressData decompresses the payload with a size limit using pooled buffers.
func DecompressData(payload []byte, limit int64) ([]byte, error) {
	reader := io.MultiReader(
		bytes.NewReader(payload),
		bytes.NewReader([]byte{0x00, 0x00, 0xff, 0xff}),
	)

	fr := getFlateReader(reader)
	defer putFlateReader(fr)

	// Start with a reasonable initial size (e.g., 4KB or payload size * 2)
	initialSize := len(payload) * 2
	if initialSize < 4096 {
		initialSize = 4096
	}
	buf := getPayloadBuffer(initialSize)

	var offset int
	for {
		// Ensure we have space to read
		if offset >= len(buf) || len(buf) == offset {
			// Grow buffer
			newSize := len(buf) * 2
			// Check limit
			if limit > 0 && int64(newSize) > limit {
				newSize = int(limit) + 1 // Allow reading one more byte to detect overflow
			}

			// If we can't grow anymore and we are full
			if newSize <= len(buf) {
				putPayloadBuffer(buf)
				return nil, ErrDecompressedTooLarge
			}

			newBuf := getPayloadBuffer(newSize)
			copy(newBuf, buf)
			putPayloadBuffer(buf)
			buf = newBuf
		}

		// Read into the remaining space
		n, err := fr.Read(buf[offset:])
		offset += n

		if limit > 0 && int64(offset) > limit {
			putPayloadBuffer(buf)
			return nil, ErrDecompressedTooLarge
		}

		if err != nil {
			if err == io.EOF {
				break
			}
			putPayloadBuffer(buf)
			return nil, err
		}
	}

	// Resize the result slice to the actual data size, but keep the underlying array
	// However, getPayloadBuffer returns a slice that might be smaller than capacity (if from pool).
	// We should just return the slice up to offset.
	// IMPORTANT: The caller is responsible for calling putPayloadBuffer on this result.
	// But our pool buckets are fixed size (512, 4k, 16k, 64k).
	// If the loop finished with a buffer potentially larger than needed, that's fine for the pool.
	// usage: buf[:offset]

	return buf[:offset], nil
}
