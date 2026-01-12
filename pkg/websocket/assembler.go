package websocket

import (
	"fmt"

	"github.com/gobwas/ws"
)

// Assembler manages message reassembly and decompression state per connection.
type Assembler struct {
	cfg          *Config
	buffer       []byte
	isCompressed bool
	lastOpCode   ws.OpCode
}

// NewAssembler creates a new Assembler.
func NewAssembler(cfg *Config) *Assembler {
	return &Assembler{cfg: cfg}
}

var (
	ErrUnexpectedContinuation = fmt.Errorf("unexpected continuation frame")
	ErrExpectedContinuation   = fmt.Errorf("expected continuation frame")
	ErrMessageTooLarge        = fmt.Errorf("message too large (buffered)")
	ErrDecompressedTooLarge   = fmt.Errorf("decompressed message too large")
)

// ProcessFrame reassembles fragmented frames and handles decompression.
// Returns: data, op, complete, isReassembled, error
func (a *Assembler) ProcessFrame(header ws.Header, payload []byte) ([]byte, ws.OpCode, bool, bool, error) {
	// 1. New Message
	if a.buffer == nil {
		if header.OpCode == ws.OpContinuation {
			return nil, 0, false, false, ErrUnexpectedContinuation
		}

		a.isCompressed = (header.Rsv == 4) && a.cfg.EnableCompression
		a.lastOpCode = header.OpCode

		if header.Fin {
			if a.isCompressed {
				decompressed, err := DecompressData(payload, a.cfg.MaxFrameSize)
				if err != nil {
					return nil, 0, false, false, err
				}
				// Decompressed data is a new allocation from pool (via DecompressData)
				return decompressed, header.OpCode, true, true, nil
			}
			// Payload is from the caller (netpoll buffer or pooled buffer), passed through
			// isReassembled = false because we didn't allocate a new assemble buffer
			return payload, header.OpCode, true, false, nil
		}

		// Fragmentation detected: Get buffer from pool and copy payload
		// Start with exact size or slightly larger to reduce re-allocations
		initialSize := len(payload)
		if initialSize < 512 {
			initialSize = 512
		}
		a.buffer = getPayloadBuffer(initialSize)
		// We can't just copy(a.buffer, payload) because getPayloadBuffer might return larger cap
		// We need to reslice to len(payload) for writing, but keep cap

		// Reset buffer usage: currently 0 bytes used.
		// Actually, let's treat a.buffer as the "holding" buffer.
		// We copy the first chunk in.
		copy(a.buffer, payload)
		a.buffer = a.buffer[:len(payload)] // Slice to length

		return nil, 0, false, false, nil
	}

	// 2. Continuation
	if header.OpCode != ws.OpContinuation {
		// Protocol error: discard buffer and return
		putPayloadBuffer(a.buffer)
		a.buffer = nil
		return nil, 0, false, false, ErrExpectedContinuation
	}

	if a.cfg.MaxFrameSize > 0 && int64(len(a.buffer))+int64(len(payload)) > a.cfg.MaxFrameSize {
		putPayloadBuffer(a.buffer)
		a.buffer = nil
		return nil, 0, false, false, ErrMessageTooLarge
	}

	// Append payload to a.buffer
	required := len(a.buffer) + len(payload)
	if required > cap(a.buffer) {
		// Grow
		newSize := cap(a.buffer) * 2
		if newSize < required {
			newSize = required
		}
		// If using buckets, getPayloadBuffer handles rounding up if needed, or we just ask for exact
		// getPayloadBuffer returns a slice with cap >= size.
		newBuf := getPayloadBuffer(newSize)
		copy(newBuf, a.buffer)
		putPayloadBuffer(a.buffer)        // Return old buffer
		a.buffer = newBuf[:len(a.buffer)] // Restore length
	}

	// Append
	currentLen := len(a.buffer)
	// Grow slice to fit new data
	a.buffer = a.buffer[:currentLen+len(payload)]
	copy(a.buffer[currentLen:], payload)

	if header.Fin {
		fullPayload := a.buffer
		// Nullify a.buffer so we don't hold it, but we hold it in fullPayload
		a.buffer = nil

		// If compressed, decompress
		if a.isCompressed {
			decompressed, err := DecompressData(fullPayload, a.cfg.MaxFrameSize)
			// Return assembled buffer to pool immediately
			putPayloadBuffer(fullPayload)

			if err != nil {
				return nil, 0, false, false, err
			}
			return decompressed, a.lastOpCode, true, true, nil
		}

		// If NOT compressed, return fullPayload directly.
		// It is fully reassembled and owns its memory (from pool).
		return fullPayload, a.lastOpCode, true, true, nil
	}

	return nil, 0, false, false, nil
}
