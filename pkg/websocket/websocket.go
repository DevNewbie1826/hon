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
	DefaultChunkSize    = 64 * 1024
	DefaultMaxFrameSize = 10 * 1024 * 1024

	headerUpgrade       = "Upgrade"
	headerConnection    = "Connection"
	headerSecVersion    = "Sec-WebSocket-Version"
	headerSecKey        = "Sec-WebSocket-Key"
	headerSecExtensions = "Sec-WebSocket-Extensions"
	valWebsocket        = "websocket"
	valVersion13        = "13"
)

type Config struct {
	MaxFrameSize      int64
	CheckOrigin       func(r *http.Request) bool
	Header            http.Header
	Cookies           []*http.Cookie
	EnableCompression bool
}

type Option func(*Config)

func WithEnableCompression(enable bool) Option {
	return func(c *Config) {
		c.EnableCompression = enable
	}
}

func WithHeader(key, value string) Option {
	return func(c *Config) {
		if c.Header == nil {
			c.Header = make(http.Header)
		}
		c.Header.Add(key, value)
	}
}

func WithCookie(cookie *http.Cookie) Option {
	return func(c *Config) {
		c.Cookies = append(c.Cookies, cookie)
	}
}

func computeAcceptKey(challenge string) string {
	h := sha1.New()
	h.Write([]byte(challenge))
	h.Write(magicGUID)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func parseHeader(b []byte) (h ws.Header, headerBytes int, err error) {
	if len(b) < 2 {
		return h, 0, nil
	}

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
		headerBytes = 4
	case 127:
		headerBytes = 10
	default:
		h.Length = int64(length)
	}

	if h.Masked {
		headerBytes += 4
	}

	if len(b) < headerBytes {
		return h, 0, nil
	}

	switch length {
	case 126:
		h.Length = int64(binary.BigEndian.Uint16(b[2:4]))
	case 127:
		h.Length = int64(binary.BigEndian.Uint64(b[2:10]))
	}

	if h.Masked {
		copy(h.Mask[:], b[headerBytes-4:headerBytes])
	}

	return h, headerBytes, nil
}

func readHeaderZeroAlloc(br *bufio.Reader) (h ws.Header, err error) {
	b, err := br.Peek(2)
	if err != nil {
		return h, err
	}

	_, headerBytes, _ := parseHeader(b)
	if headerBytes == 0 {
		lengthBits := b[1] & 0x7f
		headerBytes = 2
		if lengthBits == 126 {
			headerBytes = 4
		} else if lengthBits == 127 {
			headerBytes = 10
		}
		if b[1]&0x80 != 0 {
			headerBytes += 4
		}
	}

	b, err = br.Peek(headerBytes)
	if err != nil {
		return h, err
	}

	h, actualBytes, err := parseHeader(b)
	if err != nil {
		return h, err
	}
	if actualBytes == 0 {
		return h, io.ErrUnexpectedEOF
	}

	_, err = br.Discard(actualBytes)
	return h, err
}

func Upgrade(w http.ResponseWriter, r *http.Request, handler Handler, opts ...Option) error {
	cfg := &Config{
		MaxFrameSize: DefaultMaxFrameSize,
		CheckOrigin:  defaultCheckOrigin,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	if r.Method != http.MethodGet {
		return fmt.Errorf("websocket: method not allowed")
	}

	if !strings.EqualFold(r.Header.Get(headerConnection), headerUpgrade) ||
		!strings.EqualFold(r.Header.Get(headerUpgrade), valWebsocket) {
		return fmt.Errorf("websocket: invalid upgrade headers")
	}

	if r.Header.Get(headerSecVersion) != valVersion13 {
		return fmt.Errorf("websocket: unsupported version (need 13)")
	}

	challengeKey := r.Header.Get(headerSecKey)
	if challengeKey == "" {
		return fmt.Errorf("websocket: missing sec-websocket-key")
	}

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
	respExtensions := ""
	compressionEnabled := false
	if cfg.EnableCompression && strings.Contains(r.Header.Get(headerSecExtensions), "per-message-deflate") {
		compressionEnabled = true
		respExtensions = "\r\n" + headerSecExtensions + ": per-message-deflate; client_no_context_takeover; server_no_context_takeover"
	}

	fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\n"+
		"Upgrade: websocket\r\n"+
		"Connection: Upgrade\r\n"+
		"Sec-WebSocket-Accept: %s%s\r\n\r\n",
		acceptKey, respExtensions)

	if err := rw.Flush(); err != nil {
		conn.Close()
		return err
	}

	handler.OnOpen(conn)
	cfg.EnableCompression = compressionEnabled
	assembler := NewAssembler(cfg)

	hijacker.SetReadHandler(func(c net.Conn, rw *bufio.ReadWriter) error {
		return ServeConn(c, rw, handler, cfg, assembler)
	})

	return nil
}

func defaultCheckOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := http.NewRequest("GET", origin, nil)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func ServeConn(c net.Conn, rw *bufio.ReadWriter, handler Handler, cfg *Config, assembler *Assembler) error {
	nc, isNetpoll := c.(netpoll.Connection)

	for {
		if isNetpoll && !nc.IsActive() {
			handler.OnClose(c, nil)
			return io.EOF
		}

		var available int
		if rw != nil {
			available = rw.Reader.Buffered()
			if isNetpoll {
				available += nc.Reader().Len()
			}
		} else if isNetpoll {
			available = nc.Reader().Len()
		}

		if isNetpoll && available < 2 {
			return nil
		}

		var h ws.Header
		var headerBytes int
		var err error

		if rw != nil {
			h, err = readHeaderZeroAlloc(rw.Reader)
		} else if isNetpoll {
			b, pErr := nc.Reader().Peek(min(available, 14))
			if pErr != nil {
				return nil
			}
			h, headerBytes, _ = parseHeader(b)
			if headerBytes == 0 || available < headerBytes+int(h.Length) {
				return nil
			}
			nc.Reader().Skip(headerBytes)
		}

		if err != nil {
			if err == io.EOF {
				handler.OnClose(c, nil)
			} else {
				handler.OnClose(c, err)
			}
			return err
		}

		if cfg.MaxFrameSize > 0 && h.Length > cfg.MaxFrameSize {
			err := fmt.Errorf("frame too large")
			handler.OnClose(c, err)
			return err
		}

		// Read Payload (Zero-Copy for Netpoll)
		var payload []byte
		var pooled bool
		if rw != nil {
			payload = getPayloadBuffer(int(h.Length))
			pooled = true
			_, err = io.ReadFull(rw.Reader, payload)
		} else if isNetpoll {
			// Real Zero-Copy: Use Netpoll buffer directly
			payload, err = nc.Reader().Next(int(h.Length))
		}

		if err != nil {
			if pooled {
				putPayloadBuffer(payload)
			}
			handler.OnClose(c, err)
			return err
		}

		if err := processFrame(c, h, payload, handler, assembler); err != nil {
			if pooled {
				putPayloadBuffer(payload)
			}
			return err
		}

		if pooled {
			putPayloadBuffer(payload)
		}

		if !isNetpoll {
			if rw != nil && rw.Reader.Buffered() == 0 {
				_, err := rw.Reader.Peek(1)
				if err != nil {
					if err == io.EOF {
						handler.OnClose(c, nil)
					} else {
						handler.OnClose(c, err)
					}
					return err
				}
				break
			}
		} else {
			if available <= 0 {
				break
			}
		}
	}
	return nil
}

func processFrame(c net.Conn, h ws.Header, payload []byte, handler Handler, assembler *Assembler) error {
	if h.Masked {
		ws.Cipher(payload, h.Mask, 0)
	}
	if h.OpCode.IsControl() {
		switch h.OpCode {
		case ws.OpClose:
			handler.OnClose(c, nil)
			c.Close()
			return io.EOF
		case ws.OpPing:
			handler.OnPing(c, payload)
			wsutil.WriteServerMessage(c, ws.OpPong, payload)
		case ws.OpPong:
			handler.OnPong(c, payload)
		}
		return nil
	}
	fullPayload, op, complete, isReassembled, err := assembler.ProcessFrame(h, payload)
	if err != nil {
		handler.OnClose(c, err)
		return err
	}
	if complete {
		handler.OnMessage(c, op, fullPayload)
		// If isReassembled is true, fullPayload is from the pool and disjoint from 'payload'.
		// We must return it to the pool.
		if isReassembled {
			putPayloadBuffer(fullPayload)
		}
	}
	return nil
}
