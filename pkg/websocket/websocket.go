package websocket

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/DevNewbie1826/hon/pkg/adaptor"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

var (
	// Magic GUID for WebSocket handshake as per RFC 6455
	magicGUID = []byte("258EAFA5-E914-47DA-95CA-C5AB0DC85B11")
)

const (
	MaxControlFrameSize = 125
	DefaultChunkSize    = 64 * 1024 // 64KB
)

// Config holds the configuration for the WebSocket upgrade.
type Config struct {
	// MaxFrameSize limits the maximum payload size of a single frame.
	// If a frame header indicates a size larger than this, the connection is closed.
	// 0 means no limit (useful for streaming huge files).
	MaxFrameSize int64
}

// Option serves as a functional option for configuration.
type Option func(*Config)

// WithMaxFrameSize sets the maximum allowed size for a single frame header.
// This is useful to prevent malicious clients from claiming they will send TBs of data.
func WithMaxFrameSize(size int64) Option {
	return func(c *Config) {
		c.MaxFrameSize = size
	}
}

func computeAcceptKey(challenge string) string {
	h := sha1.New()
	h.Write([]byte(challenge))
	h.Write(magicGUID)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// readHeaderZeroAlloc parses the WebSocket frame header directly from *bufio.Reader.
// It uses Peek/Discard to achieve 0 allocations.
func readHeaderZeroAlloc(br *bufio.Reader) (h ws.Header, err error) {
	// 1. Peek first 2 bytes (Fin/Op, Mask/Len)
	b, err := br.Peek(2)
	if err != nil {
		return h, err
	}

	h.Fin = b[0]&0x80 != 0
	h.Rsv = (b[0] & 0x70) >> 4
	h.OpCode = ws.OpCode(b[0] & 0x0f)
	h.Masked = b[1]&0x80 != 0
	length := b[1] & 0x7f

	headerBytes := 2

	// 2. Parse Length
	switch length {
	case 126:
		// Next 2 bytes are length
		b, err = br.Peek(4)
		if err != nil {
			return h, err
		}
		h.Length = int64(binary.BigEndian.Uint16(b[2:4]))
		headerBytes = 4
	case 127:
		// Next 8 bytes are length
		b, err = br.Peek(10)
		if err != nil {
			return h, err
		}
		h.Length = int64(binary.BigEndian.Uint64(b[2:10]))
		headerBytes = 10
	default:
		h.Length = int64(length)
	}

	// 3. Parse Mask Key
	if h.Masked {
		// Need 4 more bytes for mask
		total := headerBytes + 4
		b, err = br.Peek(total)
		if err != nil {
			return h, err
		}
		copy(h.Mask[:], b[headerBytes:total])
	headerBytes = total
	}

	// 4. Consume bytes from buffer
	_, err = br.Discard(headerBytes)
	return h, err
}

// Upgrade performs a Zero-Copy WebSocket upgrade.
func Upgrade(w http.ResponseWriter, r *http.Request, handler Handler, opts ...Option) error {
	cfg := &Config{
		MaxFrameSize: 0, // Default: Unlimited (Streaming)
	}
	for _, opt := range opts {
		opt(cfg)
	}

	if r.Method != http.MethodGet {
		return fmt.Errorf("websocket: method not allowed")
	}

	// 1. Basic Validation
	if !strings.EqualFold(r.Header.Get("Connection"), "Upgrade") ||
		!strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return fmt.Errorf("websocket: invalid upgrade headers")
	}

	challengeKey := r.Header.Get("Sec-WebSocket-Key")
	if challengeKey == "" {
		return fmt.Errorf("websocket: missing sec-websocket-key")
	}

	// 2. Hijack Connection
	hijacker, ok := w.(adaptor.Hijacker)
	if !ok {
		return fmt.Errorf("websocket: server does not support hijacking")
	}

	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return err
	}

	// 3. Fast Handshake Response
	acceptKey := computeAcceptKey(challengeKey)
	rw.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	rw.WriteString("Upgrade: websocket\r\n")
	rw.WriteString("Connection: Upgrade\r\n")
	rw.WriteString("Sec-WebSocket-Accept: " + acceptKey + "\r\n")
	rw.WriteString("\r\n")

	if err := rw.Flush(); err != nil {
		conn.Close()
		return err
	}

	// Trigger OnOpen
	handler.OnOpen(conn)

	// 4. Set Read Handler (Event-Driven Reactor Mode)
	hijacker.SetReadHandler(func(c net.Conn, rw *bufio.ReadWriter) error {
		br := rw.Reader

		for {
			// Check Connection State (Netpoll specific)
			if ac, ok := c.(interface{ IsActive() bool }); ok && !ac.IsActive() {
				handler.OnClose(c, nil)
				return io.EOF
			}

			// Read Header (Zero Allocation)
			header, err := readHeaderZeroAlloc(br)
			if err != nil {
				if err != io.EOF {
					handler.OnClose(c, err)
				} else {
					handler.OnClose(c, nil)
				}
				return err
			}

			// Security: Validate MaxFrameSize
			if cfg.MaxFrameSize > 0 && header.Length > cfg.MaxFrameSize {
				err := fmt.Errorf("frame too large: %d > %d", header.Length, cfg.MaxFrameSize)
				handler.OnClose(c, err)
				return err
			}

			// Handle Control Frames (Ping, Pong, Close)
			if header.OpCode.IsControl() {
				if header.Length > MaxControlFrameSize {
					err := fmt.Errorf("control frame too large: %d", header.Length)
					handler.OnClose(c, err)
					return err
				}
			
				payload := make([]byte, header.Length)
				if _, err := io.ReadFull(br, payload); err != nil {
					handler.OnClose(c, err)
					return err
				}
				if header.Masked {
					ws.Cipher(payload, header.Mask, 0)
				}

				switch header.OpCode {
				case ws.OpClose:
					handler.OnClose(c, nil)
					return io.EOF
				case ws.OpPing:
					handler.OnPing(c, payload)
					_ = wsutil.WriteServerMessage(c, ws.OpPong, payload)
				case ws.OpPong:
					handler.OnPong(c, payload)
				}
			
				if br.Buffered() == 0 {
					break
				}
				continue
			}

			// Handle Data Frames (Text/Binary) - Streaming Mode
			remaining := header.Length
			maskOffset := 0

			// Handle 0-length messages
			if remaining == 0 {
				handler.OnMessage(c, header.OpCode, nil, header.Fin)
			}

			for remaining > 0 {
				// Use builtin min() (Go 1.21+) for cleaner code
				chunkSize := min(remaining, int64(DefaultChunkSize))

				payload := getPayloadBuffer(int(chunkSize))
				n, err := io.ReadFull(br, payload)
				if err != nil {
					putPayloadBuffer(payload)
					handler.OnClose(c, err)
					return err
				}

				if header.Masked {
					ws.Cipher(payload, header.Mask, maskOffset)
					maskOffset += n
				}

				remaining -= int64(n)
				
				// Final chunk is when it's the frame's Fin AND no more data remains in this frame
				isFinalChunk := header.Fin && (remaining == 0)

				handler.OnMessage(c, header.OpCode, payload, isFinalChunk)

				putPayloadBuffer(payload)
			}

			// Continue reading if there's more in the buffer
			if br.Buffered() == 0 {
				break
			}
		}
		return nil
	})

	return nil
}