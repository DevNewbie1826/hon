package websocket

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/netpoll"
)

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
	if scheme != "ws" && scheme != "wss" {
		return fmt.Errorf("unsupported scheme: %s", scheme)
	}

	host := parsedURL.Host
	if !strings.Contains(host, ":") {
		if scheme == "wss" {
			host += ":443"
		} else {
			host += ":80"
		}
	}

	dialer := netpoll.NewDialer()
	conn, err := dialer.DialConnection("tcp", host, c.DialTimeout)
	if err != nil {
		return fmt.Errorf("dial failed: %w", err)
	}

	cfg := &Config{MaxFrameSize: 10 * 1024 * 1024} // Default 10MB limit
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

	// Register Netpoll Close Callback to ensure OnClose is called on disconnect
	conn.AddCloseCallback(func(connection netpoll.Connection) error {
		wrappedHandler.OnClose(connection, fmt.Errorf("connection closed"))
		return nil
	})

	// State Machine for Handshake
	handshakeDone := make(chan error, 1)
	isHandshake := true

	err = conn.SetOnRequest(func(ctx context.Context, connection netpoll.Connection) error {
		if isHandshake {
			reader := connection.Reader()

			l := reader.Len()
			if l == 0 {
				return nil
			}

			// Fix: Check Peek error
			peekBuf, err := reader.Peek(l)
			if err != nil {
				// If peek fails, we can't process. Signal error if possible.
				select {
				case handshakeDone <- fmt.Errorf("peek failed: %w", err):
				default:
				}
				return err
			}

			// Search for end of headers
			idx := strings.Index(string(peekBuf), "\r\n\r\n")
			if idx == -1 {
				// Not enough data yet
				return nil
			}

			// We have full headers.
			headerBytes := idx + 4
			headerStr := string(peekBuf[:idx])

			// Parse Status Line
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

			// Consume data from reader
			reader.Skip(headerBytes)

			// Fix: Race Condition - Update state BEFORE signaling
			isHandshake = false

			// Trigger OnOpen
			wrappedHandler.OnOpen(connection)

			// Signal success
			select {
			case handshakeDone <- nil:
			default:
			}

			// If there is more data (frames) after headers, process them now
			if reader.Len() > 0 {
				return ServeReactor(connection, wrappedHandler, cfg)
			}
			return nil
		}

		// Frame Processing
		// Fix: Check connection state
		if !connection.IsActive() {
			wrappedHandler.OnClose(connection, nil)
			return nil
		}

		return ServeReactor(connection, wrappedHandler, cfg)
	})

	if err != nil {
		conn.Close()
		c.wg.Done() // Decrement since we failed to setup
		return fmt.Errorf("failed to set handler: %w", err)
	}

	// Send Handshake Request
	if err := sendHandshake(conn, parsedURL); err != nil {
		conn.Close()
		c.wg.Done()
		return err
	}

	// Wait for Handshake
	select {
	case err := <-handshakeDone:
		if err != nil {
			// Ensure handler cleanup if handshake failed logic didn't trigger close
			conn.Close()
			// wg done is handled by OnClose wrapper if OnClose is called,
			// but here we force close.
			// Ideally, OnClose should be called.
			wrappedHandler.OnClose(conn, err)
			return err
		}
		// Success
		return nil
	case <-time.After(c.DialTimeout):
		// Fix: Remove handler before closing to avoid race
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

func sendHandshake(conn netpoll.Connection, u *url.URL) error {
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return err
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
	sb.WriteString("Sec-WebSocket-Version: 13\r\n\r\n")

	writer := conn.Writer()
	writer.WriteString(sb.String())

	return writer.Flush()
}
