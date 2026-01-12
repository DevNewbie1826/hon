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
	"github.com/cloudwego/netpoll"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

var (
	magicGUID = []byte("258EAFA5-E914-47DA-95CA-C5AB0DC85B11")
)

const (
	MaxControlFrameSize = 125
	DefaultChunkSize    = 64 * 1024 // 64KB
	DefaultMaxFrameSize = 10 * 1024 * 1024 // 10MB
)

type Config struct {
	MaxFrameSize int64
	CheckOrigin  func(r *http.Request) bool
}

type Option func(*Config)

func WithMaxFrameSize(size int64) Option {
	return func(c *Config) {
		c.MaxFrameSize = size
	}
}

func WithCheckOrigin(f func(r *http.Request) bool) Option {
	return func(c *Config) {
		c.CheckOrigin = f
	}
}

func computeAcceptKey(challenge string) string {
	h := sha1.New()
	h.Write([]byte(challenge))
	h.Write(magicGUID)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func readHeaderZeroAlloc(br *bufio.Reader) (h ws.Header, err error) {
	b, err := br.Peek(2)
	if err != nil {
		return h, err
	}

	h.Fin = b[0]&0x80 != 0
	h.Rsv = (b[0] & 0x70) >> 4
	h.OpCode = ws.OpCode(b[0] & 0x0f)
	h.Masked = b[1]&0x80 != 0
	length := b[1] & 0x7f

	if h.OpCode.IsControl() && length > 125 {
		return h, fmt.Errorf("control frame length > 125")
	}

	headerBytes := 2

	switch length {
	case 126:
		b, err = br.Peek(4)
		if err != nil {
			return h, err
		}
		h.Length = int64(binary.BigEndian.Uint16(b[2:4]))
		headerBytes = 4
	case 127:
		b, err = br.Peek(10)
		if err != nil {
			return h, err
		}
		h.Length = int64(binary.BigEndian.Uint64(b[2:10]))
		headerBytes = 10
	default:
		h.Length = int64(length)
	}

	if h.Masked {
		total := headerBytes + 4
		b, err = br.Peek(total)
		if err != nil {
			return h, err
		}
		copy(h.Mask[:], b[headerBytes:total])
		headerBytes = total
	}

	_, err = br.Discard(headerBytes)
	return h, err
}

func readHeaderNetpoll(r netpoll.Reader) (h ws.Header, headerBytes int, err error) {
	if r.Len() < 2 {
		return h, 0, nil
	}
	b, _ := r.Peek(2)

	h.Fin = b[0]&0x80 != 0
	h.Rsv = (b[0] & 0x70) >> 4
	h.OpCode = ws.OpCode(b[0] & 0x0f)
	h.Masked = b[1]&0x80 != 0
	length := b[1] & 0x7f

	if h.OpCode.IsControl() && length > 125 {
		return h, 0, fmt.Errorf("control frame length > 125")
	}

	headerBytes = 2

	switch length {
	case 126:
		if r.Len() < 4 {
			return h, 0, nil
		}
		b, _ = r.Peek(4)
		h.Length = int64(binary.BigEndian.Uint16(b[2:4]))
		headerBytes = 4
	case 127:
		if r.Len() < 10 {
			return h, 0, nil
		}
		b, _ = r.Peek(10)
		h.Length = int64(binary.BigEndian.Uint64(b[2:10]))
		headerBytes = 10
	default:
		h.Length = int64(length)
	}

	if h.Masked {
		total := headerBytes + 4
		if r.Len() < total {
			return h, 0, nil
		}
		b, _ = r.Peek(total)
		copy(h.Mask[:], b[headerBytes:total])
		headerBytes = total
	}
	return h, headerBytes, nil
}

func Upgrade(w http.ResponseWriter, r *http.Request, handler Handler, opts ...Option) error {
	cfg := &Config{
		MaxFrameSize: DefaultMaxFrameSize,
		CheckOrigin: func(r *http.Request) bool {
			// Default: Allow only same origin or empty origin
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true
			}
			u, err := http.NewRequest("GET", origin, nil) // Hack to parse origin
			if err != nil {
				return false
			}
			return u.Host == r.Host
		},
	}
	for _, opt := range opts {
		opt(cfg)
	}

	if r.Method != http.MethodGet {
		return fmt.Errorf("websocket: method not allowed")
	}

	if !strings.EqualFold(r.Header.Get("Connection"), "Upgrade") ||
		!strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return fmt.Errorf("websocket: invalid upgrade headers")
	}

	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		return fmt.Errorf("websocket: unsupported version (need 13)")
	}

	challengeKey := r.Header.Get("Sec-WebSocket-Key")
	if challengeKey == "" {
		return fmt.Errorf("websocket: missing sec-websocket-key")
	}

	// Security: Check Origin
	if cfg.CheckOrigin != nil && !cfg.CheckOrigin(r) {
		return fmt.Errorf("websocket: origin not allowed")
	}

	hijacker, ok := w.(adaptor.Hijacker)
	if !ok {
		return fmt.Errorf("websocket: server does not support hijacking")
	}

	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return err
	}

	acceptKey := computeAcceptKey(challengeKey)
	rw.WriteString("HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + acceptKey + "\r\n\r\n")

		if err := rw.Flush(); err != nil {

			conn.Close()

			return err

		}

	

		handler.OnOpen(conn)

	

		hijacker.SetReadHandler(func(c net.Conn, rw *bufio.ReadWriter) error {

			return ServeConn(c, rw, handler, cfg)

		})

	

		return nil

	}

	

	func ServeConn(c net.Conn, rw *bufio.ReadWriter, handler Handler, cfg *Config) error {

		br := rw.Reader

	

		for {

			if ac, ok := c.(interface{ IsActive() bool }); ok && !ac.IsActive() {

				handler.OnClose(c, nil)

				return io.EOF

			}

	

			header, err := readHeaderZeroAlloc(br)

			if err != nil {

				if err != io.EOF {

					handler.OnClose(c, err)

				} else {

					handler.OnClose(c, nil)

				}

				return err

			}

	

			if cfg.MaxFrameSize > 0 && header.Length > cfg.MaxFrameSize {

				err := fmt.Errorf("frame too large: %d > %d", header.Length, cfg.MaxFrameSize)

				handler.OnClose(c, err)

				return err

			}

	

			if header.OpCode.IsControl() {

				payload := getPayloadBuffer(int(header.Length))

				if _, err := io.ReadFull(br, payload); err != nil {

					putPayloadBuffer(payload)

					handler.OnClose(c, err)

					return err

				}

				if header.Masked {

					ws.Cipher(payload, header.Mask, 0)

				}

	

				switch header.OpCode {

				case ws.OpClose:

					handler.OnClose(c, nil)

					putPayloadBuffer(payload)

					return io.EOF

				case ws.OpPing:

					handler.OnPing(c, payload)

					wsutil.WriteServerMessage(c, ws.OpPong, payload)

				case ws.OpPong:

					handler.OnPong(c, payload)

				}

				putPayloadBuffer(payload)

				continue

			}

	

			remaining := header.Length

			maskOffset := 0

			if remaining == 0 {

				handler.OnMessage(c, header.OpCode, nil, header.Fin)

			}

	

			for remaining > 0 {

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

				isFinalChunk := header.Fin && (remaining == 0)

				handler.OnMessage(c, header.OpCode, payload, isFinalChunk)

				putPayloadBuffer(payload)

			}

		}

	}

	

	func ServeReactor(c netpoll.Connection, handler Handler, cfg *Config) error {

		reader := c.Reader()

	

		for {

			if !c.IsActive() {

				handler.OnClose(c, nil)

				return nil

			}

	

			if reader.Len() == 0 {

				return nil

			}

	

			header, headerBytes, err := readHeaderNetpoll(reader)

			if err != nil {

				handler.OnClose(c, err)

				return err

			}

			if headerBytes == 0 {

				return nil // Need more data for header

			}

	

			// Security: Check MaxFrameSize

			if cfg.MaxFrameSize > 0 && header.Length > cfg.MaxFrameSize {

				err := fmt.Errorf("frame too large: %d > %d", header.Length, cfg.MaxFrameSize)

				handler.OnClose(c, err)

				return err

			}

	

			// Payload Check

			totalFrameSize := headerBytes + int(header.Length)

			if reader.Len() < totalFrameSize {

				return nil // Need more data for payload

			}

	

			// Consume Header

			reader.Skip(headerBytes)

	

			// Consume Payload (Zero-Copy)

			// We use Next() which might return a slice pointing to the buffer.

			// Important: If we use sync.Pool, we should NOT put this buffer back if it belongs to Netpoll.

			// Netpoll's Next() returns a slice of the underlying buffer or a copy. 

			// Since we need to mask/unmask in place, we should be careful.

			// However, ws.Cipher modifies in-place.

			

			// To be safe and consistent with our Zero-Alloc philosophy using OUR pools:

			// But wait, Netpoll manages its own memory.

			// If we use Next(), we get data. If we modify it (unmask), it's fine for this message.

			

			// The issue 2.8 was about control frames payload not being put back IF we allocated it.

			// In ServeReactor, we used reader.Next(). We did NOT allocate from our pool.

			// So we do NOT need to put it back to OUR pool.

			// BUT, we need to handle the control flow correctly.

	

			payload, err := reader.Next(int(header.Length))

			if err != nil {

				handler.OnClose(c, err)

				return err

			}

	

			if header.Masked {

				ws.Cipher(payload, header.Mask, 0)

			}

	

			if header.OpCode.IsControl() {

				switch header.OpCode {

				case ws.OpClose:

					handler.OnClose(c, nil)

					// ServeReactor returns nil to keep the connection open? No, close frame means close.

					// Returning nil might restart the loop? No, typically we should close.

					// But Reactor logic depends on caller. Usually return nil means "handled, wait for more".

					// But for Close Op, we should probably stop.

					// Let's assume closing connection is the right way.

					c.Close() 

					return nil

				case ws.OpPing:

					handler.OnPing(c, payload)

					wsutil.WriteServerMessage(c, ws.OpPong, payload)

				case ws.OpPong:

					handler.OnPong(c, payload)

				}

				continue

			}

	

			handler.OnMessage(c, header.OpCode, payload, header.Fin)

		}

	}
