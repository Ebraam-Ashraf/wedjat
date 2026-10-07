// Package client provides a daemon socket client with lazy connection,
// automatic reconnection, and wire protocol decoding.
package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/Ebraam-Ashraf/wedjat/internal/wire"
)

// Client connects to the wedjat daemon Unix socket and decodes wire messages.
type Client struct {
	socketPath string

	mu         sync.Mutex
	conn       net.Conn
	reader     *bufio.Reader
	reconnect  bool
	reconnectCh chan struct{}

	// Callbacks for each message type
	OnGPU     func([]wire.GPUSample)
	OnProcs   func(*wire.ProcList)
	OnXid     func(*wire.Xid)
	OnAgg     func([]wire.AggRow)
	OnEvent   func(*wire.Event)
}

// New creates a new daemon socket client.
func New(socketPath string) *Client {
	return &Client{
		socketPath:  socketPath,
		reconnectCh: make(chan struct{}, 1),
	}
}

// Start begins connecting to the daemon and processing messages.
// It returns immediately; connection happens lazily on first use or
// when messages are available.
func (c *Client) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		return nil // already connected
	}

	c.reconnect = true
	go c.connectLoop(ctx)
	return nil
}

// Stop closes the connection and stops reconnection attempts.
func (c *Client) Stop() error {
	c.mu.Lock()
	c.reconnect = false
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.mu.Unlock()

	select {
	case c.reconnectCh <- struct{}{}:
	default:
	}
	return nil
}

// connectLoop handles connection and reconnection with exponential backoff.
func (c *Client) connectLoop(ctx context.Context) {
	backoff := 500 * time.Millisecond
	maxBackoff := 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		c.mu.Lock()
		reconnect := c.reconnect
		c.mu.Unlock()

		if !reconnect {
			return
		}

		conn, err := net.Dial("unix", c.socketPath)
		if err != nil {
			time.Sleep(backoff)
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		backoff = 500 * time.Millisecond

		c.mu.Lock()
		c.conn = conn
		c.reader = bufio.NewReader(conn)
		c.mu.Unlock()

		if err := c.readLoop(ctx); err != nil {
			c.mu.Lock()
			if c.conn == conn {
				c.conn.Close()
				c.conn = nil
				c.reader = nil
			}
			c.mu.Unlock()
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// readLoop reads and decodes messages from the socket.
func (c *Client) readLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		c.mu.Lock()
		reader := c.reader
		c.mu.Unlock()

		if reader == nil {
			return errors.New("connection closed")
		}

		line, err := reader.ReadBytes('\n')
		if err != nil {
			return err
		}

		env, err := wire.Decode(line)
		if err != nil {
			continue // skip malformed messages
		}

		c.dispatch(env)
	}
}

// dispatch routes the envelope to the appropriate callback.
func (c *Client) dispatch(env wire.Envelope) {
	switch env.Type {
	case wire.TypeGPU:
		if c.OnGPU != nil {
			var msg []wire.GPUSample
			if json.Unmarshal(env.Data, &msg) == nil {
				c.OnGPU(msg)
			}
		}
	case wire.TypeProcs:
		if c.OnProcs != nil {
			var msg wire.ProcList
			if json.Unmarshal(env.Data, &msg) == nil {
				c.OnProcs(&msg)
			}
		}
	case wire.TypeXid:
		if c.OnXid != nil {
			var msg wire.Xid
			if json.Unmarshal(env.Data, &msg) == nil {
				c.OnXid(&msg)
			}
		}
	case wire.TypeAgg:
		if c.OnAgg != nil {
			var msg []wire.AggRow
			if json.Unmarshal(env.Data, &msg) == nil {
				c.OnAgg(msg)
			}
		}
	case wire.TypeEvent:
		if c.OnEvent != nil {
			var msg wire.Event
			if json.Unmarshal(env.Data, &msg) == nil {
				c.OnEvent(&msg)
			}
		}
	}
}

// IsConnected returns true if the client is currently connected.
func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil
}