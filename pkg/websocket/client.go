package websocket

import (
	"bytes"
	"context"
	"crypto/rand"
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

// Client manages WebSocket connections.
type Client struct {
	DialTimeout time.Duration
	// wg tracks active connections for graceful shutdown.
	wg sync.WaitGroup
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
				return err
			}
			idx := bytes.Index(peekBuf, []byte("\r\n\r\n"))
			if idx == -1 {
				if available > maxHeaderSize {
					return fmt.Errorf("websocket: response header too large")
				}
				return nil
			}
			headerBytes := idx + 4
			headerStr := string(peekBuf[:idx])
			lines := strings.Split(headerStr, "\r\n")
			if len(lines) < 1 {
				err := fmt.Errorf("empty response")
				select {
				case handshakeDone <- err:
				default:
				}
				return nil
			}
			if !strings.HasPrefix(lines[0], "HTTP/1.1 101") {
				err := fmt.Errorf("handshake failed: %s", lines[0])
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
	if err := sendHandshake(conn, parsedURL, cfg); err != nil {
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

func buildHandshakeRequest(u *url.URL, cfg *Config) (string, error) {
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return "", err
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
	return sb.String(), nil
}

func sendHandshake(conn netpoll.Connection, u *url.URL, cfg *Config) error {
	req, err := buildHandshakeRequest(u, cfg)
	if err != nil {
		return err
	}
	writer := conn.Writer()
	writer.WriteString(req)
	return writer.Flush()
}
