// Package cdp is a minimal DevTools Protocol client, written to leave fewer
// traces than a general-purpose one.
//
// It exists for a single reason: general-purpose drivers (chromedp among them)
// call Runtime.enable unconditionally while attaching to a target, and anti-bot
// services probe for exactly that. The call is load-bearing inside them — it is
// how they tell a worker target from a page — and cannot be switched off
// through their APIs, so the only way to avoid it is to speak the protocol
// directly.
//
// What that costs: no action pipeline, no automatic domain enabling, no target
// lifecycle management. This client sends commands and reads their replies.
// That is all the launcher needs, and every command it sends is one we chose.
package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/coder/websocket"
)

// ErrClosed is returned when the connection is gone.
var ErrClosed = errors.New("cdp connection closed")

// errProtocol carries an error the browser itself reported.
var errProtocol = errors.New("devtools protocol error")

// maxMessage bounds a single protocol frame. DOM-heavy replies are large, and
// the default limit is far too small for them.
const maxMessage = 64 << 20 // 64 MiB

// Client is a connection to a browser's DevTools endpoint.
type Client struct {
	conn *websocket.Conn

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan reply
	closed  bool

	// session is the target this client is attached to; empty addresses the
	// browser itself.
	session string

	done chan struct{}
}

type request struct {
	ID        int64  `json:"id"`
	Method    string `json:"method"`
	Params    any    `json:"params,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}

type reply struct {
	Result json.RawMessage
	Err    error
}

type frame struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Method string `json:"method"`
}

// Dial connects to a DevTools websocket endpoint.
func Dial(ctx context.Context, wsURL string) (*Client, error) {
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("connecting to devtools: %w", err)
	}
	conn.SetReadLimit(maxMessage)

	c := &Client{
		conn:    conn,
		pending: map[int64]chan reply{},
		done:    make(chan struct{}),
	}
	//nolint:gosec,contextcheck // G118: the reader serves the whole connection and is stopped by Close, not by the dial context
	go c.read()

	return c, nil
}

// read dispatches replies to their waiters. Events are discarded: nothing here
// subscribes to any, which is the point — no domain is enabled, so the browser
// sends almost nothing unprompted.
func (c *Client) read() {
	defer close(c.done)

	for {
		_, data, err := c.conn.Read(context.Background())
		if err != nil {
			c.failAll(err)

			return
		}

		var f frame
		if err := json.Unmarshal(data, &f); err != nil || f.ID == 0 {
			continue // an event, or something we do not understand
		}

		c.mu.Lock()
		ch, ok := c.pending[f.ID]
		delete(c.pending, f.ID)
		c.mu.Unlock()
		if !ok {
			continue
		}

		if f.Error != nil {
			ch <- reply{Err: fmt.Errorf("%w: %s", errProtocol, f.Error.Message)}

			continue
		}
		ch <- reply{Result: f.Result}
	}
}

func (c *Client) failAll(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.closed = true
	for id, ch := range c.pending {
		ch <- reply{Err: fmt.Errorf("%w: %w", ErrClosed, err)}
		delete(c.pending, id)
	}
}

// Send issues a command and waits for its reply.
func (c *Client) Send(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()

		return nil, ErrClosed
	}
	c.nextID++
	id := c.nextID
	ch := make(chan reply, 1)
	c.pending[id] = ch
	session := c.session
	c.mu.Unlock()

	raw, err := json.Marshal(request{ID: id, Method: method, Params: params, SessionID: session})
	if err != nil {
		return nil, fmt.Errorf("encoding %s: %w", method, err)
	}
	if err := c.conn.Write(ctx, websocket.MessageText, raw); err != nil {
		return nil, fmt.Errorf("sending %s: %w", method, err)
	}

	select {
	case r := <-ch:
		if r.Err != nil {
			return nil, fmt.Errorf("%s: %w", method, r.Err)
		}

		return r.Result, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%s: %w", method, ctx.Err())
	case <-c.done:
		return nil, fmt.Errorf("%s: %w", method, ErrClosed)
	}
}

// AttachToPage attaches to the browser's first page target and routes every
// later command to it.
//
// flatten:true keeps the session on this one connection, so no second socket is
// opened. Notably absent afterwards: Runtime.enable. A general-purpose driver
// issues it here to discover whether the target is a worker; we already know it
// is a page, because that is what we asked for.
//
// The session returned lives as long as this client. Callers must keep it open
// for as long as they want their overrides to hold — Chrome reverts every
// Emulation.* override and drops injected scripts when the session detaches.
func (c *Client) AttachToPage(ctx context.Context) error {
	raw, err := c.Send(ctx, "Target.getTargets", nil)
	if err != nil {
		return err
	}

	var targets struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
		} `json:"targetInfos"`
	}
	if err := json.Unmarshal(raw, &targets); err != nil {
		return fmt.Errorf("decoding targets: %w", err)
	}

	for _, t := range targets.TargetInfos {
		if t.Type != "page" {
			continue
		}

		raw, err := c.Send(ctx, "Target.attachToTarget", map[string]any{
			"targetId": t.TargetID,
			"flatten":  true,
		})
		if err != nil {
			return err
		}

		var attached struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(raw, &attached); err != nil {
			return fmt.Errorf("decoding session: %w", err)
		}

		c.mu.Lock()
		c.session = attached.SessionID
		c.mu.Unlock()

		return nil
	}

	return fmt.Errorf("%w: browser exposes no page target", errProtocol)
}

// Close shuts the connection down.
func (c *Client) Close() error {
	c.mu.Lock()
	already := c.closed
	c.closed = true
	c.mu.Unlock()
	if already {
		return nil
	}

	if err := c.conn.Close(websocket.StatusNormalClosure, ""); err != nil {
		return fmt.Errorf("closing devtools connection: %w", err)
	}

	return nil
}
