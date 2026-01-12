package websocket

import (
	"crypto/rand"
	"io"
	"net"

	"github.com/cloudwego/netpoll"
	"github.com/gobwas/ws"
)

type netpollWriterAdapter struct {
	w netpoll.Writer
}

func (a netpollWriterAdapter) Write(p []byte) (n int, err error) {
	return a.w.WriteBinary(p)
}

func WriteMessage(c net.Conn, op ws.OpCode, payload []byte, cfg *Config) error {
	return writeMessageInternal(c, op, payload, cfg, false)
}

// WriteClientMessage sends a message to the connection (Client -> Server, Masked).
func WriteClientMessage(c net.Conn, op ws.OpCode, payload []byte, cfg *Config) error {
	return writeMessageInternal(c, op, payload, cfg, true)
}

func writeMessageInternal(c net.Conn, op ws.OpCode, payload []byte, cfg *Config, masked bool) error {
	// 1. Compression
	const minCompressSize = 1024
	compressed := false

	header := ws.Header{
		Fin:    true,
		OpCode: op,
		Length: int64(len(payload)),
		Masked: masked,
	}

	if cfg != nil && cfg.EnableCompression && op != ws.OpPing && op != ws.OpPong && op != ws.OpClose {
		if len(payload) >= minCompressSize {
			data, err := CompressData(payload)
			if err != nil {
				return err
			}
			payload = data
			header.Length = int64(len(payload))
			header.Rsv = 4 // RSV1
			compressed = true
		}
	}

	if masked {
		// Generate mask key
		if _, err := rand.Read(header.Mask[:]); err != nil {
			return err
		}
	}

	// 2. Write Frame
	// Fallback to standard net.Conn (Blocking) to avoid netpoll.Writer.WriteBinary uncertainty
	w := c
	err := ws.WriteHeader(w, header)
	if err != nil {
		return err
	}

	if len(payload) > 0 {
		if masked {
			if !compressed {
				// Copy needed because masking mutates in-place and we don't own payload if !compressed (might be from app)
				// Use pool instead of make
				b := getPayloadBuffer(len(payload))
				copy(b, payload)
				ws.Cipher(b, header.Mask, 0)
				_, err = w.Write(b)
				putPayloadBuffer(b)
			} else {
				// If compressed, payload is already a new allocation from CompressData (or we should check ownership).
				// CompressData returns a totally new buffer (from pool).
				// So we CAN mutate it.
				ws.Cipher(payload, header.Mask, 0)
				_, err = w.Write(payload)
				// Release compressed payload
				putPayloadBuffer(payload)
			}
		} else {
			_, err = w.Write(payload)
			// If not masked but compressed
			if compressed {
				putPayloadBuffer(payload)
			}
		}
		return err
	}
	return nil
}

type appender interface {
	io.Writer
}
