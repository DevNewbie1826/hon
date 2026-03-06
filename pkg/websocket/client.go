package websocket

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/netpoll"
)

const maxHeaderSize = 4096 // 4KB for handshake header limit

var (
	headerConnectionBytes    = []byte(headerConnection)
	headerUpgradeBytes       = []byte(headerUpgrade)
	valWebsocketBytes        = []byte(valWebsocket)
	secWebSocketAcceptHeader = []byte("Sec-WebSocket-Accept")
)

// Client manages WebSocket connections.
type Client struct {
	DialTimeout time.Duration
	// wg tracks active connections for graceful shutdown.
	wg sync.WaitGroup
}

type handshakeRequest struct {
	raw    string
	secKey string
}

// NewClient creates a new WebSocket Client.
func NewClient() *Client {
	return &Client{
		DialTimeout: 10 * time.Second,
	}
}

var DefaultClient = NewClient()

func Dial(u string, handler Handler, opts ...Option) error {
	return DefaultClient.Dial(u, handler, opts...)
}

// Dial connects to a WebSocket server using Netpoll's Reactor pattern.
func (c *Client) Dial(u string, handler Handler, opts ...Option) error {
	parsedURL, err := url.Parse(u)
	if err != nil {
		return fmt.Errorf("malformed url: %w", err)
	}

	scheme := parsedURL.Scheme
	if scheme != "ws" {
		return fmt.Errorf("unsupported scheme: %s (only ws:// is supported in reactor mode)", scheme)
	}

	host := parsedURL.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}

	cfg := &Config{
		MaxFrameSize: 10 * 1024 * 1024,
		Header:       make(http.Header),
	}
	for _, opt := range opts {
		opt(cfg)
	}

	req, err := buildHandshakeRequest(parsedURL, cfg)
	if err != nil {
		return err
	}

	// Track active connection
	c.wg.Add(1)

	// Wrap handler to track Close
	wrappedHandler := &clientCloseTracker{
		Handler: handler,
		client:  c,
		once:    sync.Once{},
	}

	// WS: Netpoll Reactor Mode
	dialer := netpoll.NewDialer()
	conn, err := dialer.DialConnection("tcp", host, c.DialTimeout)
	if err != nil {
		c.wg.Done()
		return fmt.Errorf("dial failed: %w", err)
	}

	// Register Netpoll Close Callback
	conn.AddCloseCallback(func(connection netpoll.Connection) error {
		wrappedHandler.OnClose(connection, fmt.Errorf("connection closed"))
		return nil
	})

	// State Machine for Handshake
	handshakeDone := make(chan error, 1)
	isHandshake := true
	reportHandshakeErr := func(err error) error {
		select {
		case handshakeDone <- err:
		default:
		}
		return err
	}

	// Initialize Assembler once per connection to maintain state
	assembler := NewAssembler(cfg)

	err = conn.SetOnRequest(func(ctx context.Context, connection netpoll.Connection) error {
		if isHandshake {
			// ... (Handshake logic same as before) ...
			reader := connection.Reader()
			available := reader.Len()
			if available == 0 {
				return nil
			}
			peekLen := min(available, maxHeaderSize)
			peekBuf, err := reader.Peek(peekLen)
			if err != nil && err != netpoll.ErrEOF && err != io.EOF {
				return reportHandshakeErr(err)
			}
			idx := bytes.Index(peekBuf, []byte("\r\n\r\n"))
			if idx == -1 {
				if available > maxHeaderSize {
					return reportHandshakeErr(fmt.Errorf("websocket: response header too large"))
				}
				return nil
			}
			headerBytes := idx + 4
			headerBlock := peekBuf[:idx]
			statusLineEnd := bytes.Index(headerBlock, []byte("\r\n"))
			if statusLineEnd == -1 {
				err := fmt.Errorf("empty response")
				select {
				case handshakeDone <- err:
				default:
				}
				return nil
			}
			statusLine := headerBlock[:statusLineEnd]
			if !bytes.HasPrefix(statusLine, []byte("HTTP/1.1 101")) {
				err := fmt.Errorf("handshake failed: %s", string(statusLine))
				select {
				case handshakeDone <- err:
				default:
				}
				return nil
			}
			if err := validateHandshakeResponse(headerBlock[statusLineEnd+2:], req.secKey); err != nil {
				select {
				case handshakeDone <- err:
				default:
				}
				return nil
			}
			reader.Skip(headerBytes)
			isHandshake = false
			wrappedHandler.OnOpen(connection)
			select {
			case handshakeDone <- nil:
			default:
			}
			if reader.Len() > 0 {
				return ServeConn(connection, nil, wrappedHandler, cfg, assembler)
			}
			return nil
		}

		if !connection.IsActive() {
			wrappedHandler.OnClose(connection, nil)
			return nil
		}
		return ServeConn(connection, nil, wrappedHandler, cfg, assembler)
	})
	if err != nil {
		conn.Close()
		c.wg.Done()
		return fmt.Errorf("failed to set handler: %w", err)
	}

	// Send Handshake Request
	if err := sendHandshake(conn, req.raw); err != nil {
		conn.Close()
		c.wg.Done()
		return err
	}

	// Wait for Handshake
	select {
	case err := <-handshakeDone:
		if err != nil {
			conn.Close()
			wrappedHandler.OnClose(conn, err)
			return err
		}
		return nil
	case <-time.After(c.DialTimeout):
		conn.SetOnRequest(nil)
		conn.Close()
		wrappedHandler.OnClose(conn, fmt.Errorf("handshake timeout"))
		return fmt.Errorf("handshake timeout")
	}
}

// Wait waits for all active connections initiated by this client to close.
func (c *Client) Wait() {
	c.wg.Wait()
}

// clientCloseTracker ensures wg.Done() is called exactly once when connection closes
type clientCloseTracker struct {
	Handler
	client *Client
	once   sync.Once
}

func (h *clientCloseTracker) OnClose(c net.Conn, err error) {
	h.once.Do(func() {
		h.Handler.OnClose(c, err)
		h.client.wg.Done()
	})
}

func buildHandshakeRequest(u *url.URL, cfg *Config) (handshakeRequest, error) {
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return handshakeRequest{}, err
	}
	secKey := base64.StdEncoding.EncodeToString(key)

	path := u.Path
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}

	var sb strings.Builder
	sb.WriteString("GET ")
	sb.WriteString(path)
	sb.WriteString(" HTTP/1.1\r\n")
	sb.WriteString("Host: ")
	sb.WriteString(u.Host)
	sb.WriteString("\r\n")
	sb.WriteString("Upgrade: websocket\r\nConnection: Upgrade\r\n")
	sb.WriteString("Sec-WebSocket-Key: ")
	sb.WriteString(secKey)
	sb.WriteString("\r\n")
	sb.WriteString("Sec-WebSocket-Version: 13\r\n")

	if cfg.EnableCompression {
		sb.WriteString("Sec-WebSocket-Extensions: per-message-deflate; client_no_context_takeover; server_no_context_takeover\r\n")
	}

	// Add Custom Headers
	if cfg.Header != nil {
		for k, vs := range cfg.Header {
			for _, v := range vs {
				sb.WriteString(k)
				sb.WriteString(": ")
				sb.WriteString(v)
				sb.WriteString("\r\n")
			}
		}
	}

	// Add Cookies
	if len(cfg.Cookies) > 0 {
		sb.WriteString("Cookie: ")
		for i, cookie := range cfg.Cookies {
			if i > 0 {
				sb.WriteString("; ")
			}
			sb.WriteString(cookie.Name)
			sb.WriteString("=")
			sb.WriteString(cookie.Value)
		}
		sb.WriteString("\r\n")
	}

	sb.WriteString("\r\n")
	return handshakeRequest{
		raw:    sb.String(),
		secKey: secKey,
	}, nil
}

func validateHandshakeResponse(headerBlock []byte, secKey string) error {
	var connectionOK bool
	var upgradeOK bool
	var acceptKeyOK bool

	for len(headerBlock) > 0 {
		lineEnd := bytes.Index(headerBlock, []byte("\r\n"))
		var line []byte
		if lineEnd == -1 {
			line = headerBlock
			headerBlock = nil
		} else {
			line = headerBlock[:lineEnd]
			headerBlock = headerBlock[lineEnd+2:]
		}
		if len(line) == 0 {
			continue
		}

		colon := bytes.IndexByte(line, ':')
		if colon == -1 {
			continue
		}

		key := bytes.TrimSpace(line[:colon])
		value := bytes.TrimSpace(line[colon+1:])

		switch {
		case bytes.EqualFold(key, headerConnectionBytes):
			connectionOK = connectionOK || headerHasTokenBytes(value, headerUpgradeBytes)
		case bytes.EqualFold(key, headerUpgradeBytes):
			upgradeOK = upgradeOK || bytes.EqualFold(value, valWebsocketBytes)
		case bytes.EqualFold(key, secWebSocketAcceptHeader):
			acceptKeyOK = acceptKeyOK || acceptKeyMatches(value, secKey)
		}
	}

	if !connectionOK {
		return fmt.Errorf("handshake failed: missing upgrade connection token")
	}
	if !upgradeOK {
		return fmt.Errorf("handshake failed: invalid upgrade header")
	}
	if !acceptKeyOK {
		return fmt.Errorf("handshake failed: invalid accept key")
	}
	return nil
}

func headerHasToken(v, token string) bool {
	for _, part := range strings.Split(v, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

func headerHasTokenBytes(v []byte, token []byte) bool {
	for len(v) > 0 {
		comma := bytes.IndexByte(v, ',')
		var part []byte
		if comma == -1 {
			part = v
			v = nil
		} else {
			part = v[:comma]
			v = v[comma+1:]
		}
		if bytes.EqualFold(bytes.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

func bytesEqualString(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i := range b {
		if b[i] != s[i] {
			return false
		}
	}
	return true
}

func acceptKeyMatches(value []byte, secKey string) bool {
	if len(value) != 28 {
		return false
	}

	totalLen := len(secKey) + len(magicGUID)
	if totalLen > 128 {
		return bytesEqualString(value, computeAcceptKey(secKey))
	}

	var keyBuf [128]byte
	n := copy(keyBuf[:], secKey)
	copy(keyBuf[n:], magicGUID)

	sum := sha1.Sum(keyBuf[:totalLen])
	var encoded [28]byte
	base64.StdEncoding.Encode(encoded[:], sum[:])
	return bytes.Equal(value, encoded[:])
}

func sendHandshake(conn netpoll.Connection, req string) error {
	writer := conn.Writer()
	writer.WriteString(req)
	return writer.Flush()
}
