// Package conn implements the acorn client-side of the acorn protocol.
// Mirrors acorn/connection.py.
//
// Flow:
//  1. HTTP POST {host}/api/spore-code/auth with {username, key} → {token}.
//  2. WebSocket connect to {ws_host}/ws?token={token}.
//  3. Inbound messages fan out into Client.In.
//  4. On disconnect, transparent reconnect with exponential backoff,
//     re-authenticate, flush outbox of messages queued during the outage.
//
// Tool requests (server → client, name: "tool:request") are intercepted
// here and handed to ToolExecutor. Regular frames go to Client.In.
package conn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Raw inbound frame. app layer decodes by inspecting .Type and extracting
// the fields it cares about. Keeping the whole raw JSON around lets the UI
// decode structured payloads without losing shape.
type Frame struct {
	Type string          `json:"type"`
	Raw  json.RawMessage `json:"-"`
}

// Authed response from /api/spore-code/auth.
type authResp struct {
	Token string `json:"token"`
	Error string `json:"error,omitempty"`
}

// Client owns the WebSocket + reconnect logic.
type Client struct {
	host, user, key string
	port            int

	baseURL string
	wsURL   string

	mu        sync.Mutex
	ws        *websocket.Conn
	connected bool
	token     string
	outbox    [][]byte

	// Subscribers.
	In           chan Frame
	ToolRequests chan Frame // tool:request frames route here; executor consumes

	// Hooks.
	OnConnected    func()
	OnDisconnected func()
	OnReconnecting func(attempt int)
	Logger         func(level, tag, msg string)

	done chan struct{} // closed on Close()
}

func New(host string, port int, user, key string) *Client {
	base := host
	if !strings.Contains(host, "://") {
		base = fmt.Sprintf("http://%s:%d", host, port)
	}
	base = strings.TrimRight(base, "/")
	wsBase := strings.Replace(base, "https://", "wss://", 1)
	wsBase = strings.Replace(wsBase, "http://", "ws://", 1)
	return &Client{
		host:         host,
		port:         port,
		user:         user,
		key:          key,
		baseURL:      base,
		wsURL:        wsBase + "/ws",
		// In is sized for bursty streaming. 4096 covers a long agent
		// turn (1000+ chat:delta chunks) even with a slow renderer
		// (glamour render time grows linearly with message length).
		// Combined with the never-drop guard for chat:delta /
		// chat:thinking in readLoop, this prevents the "garbled text"
		// failure mode (Kimi K2.6 + glamour on a long reply).
		In:           make(chan Frame, 4096),
		ToolRequests: make(chan Frame, 32),
		done:         make(chan struct{}),
	}
}

// Authenticate POSTs to /api/spore-code/auth and stashes the token.
func (c *Client) Authenticate(ctx context.Context) error {
	payload := map[string]string{"username": c.user, "key": c.key}
	data, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/spore-code/auth", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("auth post failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var ar authResp
	_ = json.Unmarshal(body, &ar)
	if resp.StatusCode != 200 {
		if ar.Error != "" {
			return fmt.Errorf("auth %d: %s", resp.StatusCode, ar.Error)
		}
		return fmt.Errorf("auth %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	if ar.Token == "" {
		return errors.New("auth returned no token")
	}
	c.mu.Lock()
	c.token = ar.Token
	c.mu.Unlock()
	return nil
}

// Connect opens the WebSocket using the previously-authenticated token.
// Authenticate must have been called first.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	tok := c.token
	c.mu.Unlock()
	if tok == "" {
		return errors.New("connect: no token — authenticate first")
	}
	u, err := url.Parse(c.wsURL)
	if err != nil {
		return fmt.Errorf("bad ws url: %w", err)
	}
	q := u.Query()
	q.Set("token", tok)
	u.RawQuery = q.Encode()

	d := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		ReadBufferSize:   1 << 16,
		WriteBufferSize:  1 << 16,
	}
	ws, resp, err := d.DialContext(ctx, u.String(), nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		return fmt.Errorf("ws dial %s: %w (status %d)", u.String(), err, status)
	}
	c.mu.Lock()
	c.ws = ws
	c.connected = true
	c.mu.Unlock()

	go c.readLoop()
	go c.pingLoop()

	if c.OnConnected != nil {
		c.OnConnected()
	}
	return nil
}

// Send marshals and writes a message. If disconnected, queues it for the
// next reconnect — that's parity with acorn's _outbox.
func (c *Client) Send(msg map[string]any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return c.sendRaw(data)
}

func (c *Client) sendRaw(data []byte) error {
	c.mu.Lock()
	ws := c.ws
	connected := c.connected
	c.mu.Unlock()
	if ws != nil && connected {
		if err := ws.WriteMessage(websocket.TextMessage, data); err == nil {
			return nil
		}
	}
	// Queue for reconnect flush.
	c.mu.Lock()
	c.outbox = append(c.outbox, data)
	c.mu.Unlock()
	c.log("debug", "ws", fmt.Sprintf("queued message (%d in outbox)", len(c.outbox)))
	// Kick reconnect if we think we're connected but the write failed.
	go c.reconnect()
	return nil
}

// Close shuts everything down.
func (c *Client) Close() {
	select {
	case <-c.done:
		return
	default:
	}
	close(c.done)
	c.mu.Lock()
	c.connected = false
	if c.ws != nil {
		_ = c.ws.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "bye"))
		_ = c.ws.Close()
		c.ws = nil
	}
	c.mu.Unlock()
}

func (c *Client) readLoop() {
	for {
		c.mu.Lock()
		ws := c.ws
		connected := c.connected
		c.mu.Unlock()
		if ws == nil || !connected {
			return
		}
		_, data, err := ws.ReadMessage()
		if err != nil {
			c.log("warn", "ws", fmt.Sprintf("connection closed: %v", err))
			c.mu.Lock()
			c.connected = false
			c.mu.Unlock()
			if c.OnDisconnected != nil {
				c.OnDisconnected()
			}
			// Push an error frame so the UI sees the disconnect.
			select {
			case c.In <- Frame{Type: "conn:error", Raw: mustJSON(map[string]any{"type": "conn:error", "error": err.Error()})}:
			default:
			}
			go c.reconnect()
			return
		}
		var peek struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &peek); err != nil {
			continue
		}
		f := Frame{Type: peek.Type, Raw: json.RawMessage(data)}
		if peek.Type == "tool:request" {
			select {
			case c.ToolRequests <- f:
			default:
				c.log("warn", "ws", "tool:request dropped — ToolRequests channel full")
			}
			continue
		}
		// Streaming text MUST never be dropped — losing a chat:delta
		// or chat:thinking chunk corrupts the visible message body
		// (the user sees concatenated/missing words mid-sentence,
		// unrecoverable). Block-send for those; the WebSocket reader
		// pauses until the UI drains. For everything else (status
		// updates, code views, subagent events, etc.) we keep the
		// non-blocking send + drop-on-overflow behavior so a backed-up
		// UI doesn't completely freeze the connection.
		if peek.Type == "chat:delta" || peek.Type == "chat:thinking" {
			c.In <- f
			continue
		}
		select {
		case c.In <- f:
		default:
			// Non-essential frame dropped — UI is backed up.
			c.log("debug", "ws", "frame dropped (In channel full, type="+peek.Type+")")
		}
	}
}

func (c *Client) pingLoop() {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			c.mu.Lock()
			ws := c.ws
			connected := c.connected
			c.mu.Unlock()
			if ws == nil || !connected {
				return
			}
			if err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				c.log("warn", "ws", fmt.Sprintf("heartbeat failed: %v", err))
				c.mu.Lock()
				c.connected = false
				c.mu.Unlock()
				go c.reconnect()
				return
			}
		}
	}
}

// reconnect with exponential backoff, re-auth, and outbox flush. Matches
// acorn/connection.py:_reconnect.
func (c *Client) reconnect() {
	c.mu.Lock()
	if c.connected {
		c.mu.Unlock()
		return
	}
	// Lock a "reconnecting" flag by reusing the done channel pattern.
	if ws := c.ws; ws != nil {
		_ = ws.Close()
		c.ws = nil
	}
	c.mu.Unlock()

	backoff := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		15 * time.Second,
		30 * time.Second,
		30 * time.Second,
		30 * time.Second,
	}
	for attempt, d := range backoff {
		select {
		case <-c.done:
			return
		default:
		}
		if c.OnReconnecting != nil {
			c.OnReconnecting(attempt + 1)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		if err := c.Authenticate(ctx); err != nil {
			cancel()
			c.log("debug", "ws", fmt.Sprintf("reconnect auth #%d failed: %v", attempt+1, err))
			time.Sleep(d)
			continue
		}
		cancel()
		ctx2, cancel2 := context.WithTimeout(context.Background(), 12*time.Second)
		err := c.Connect(ctx2)
		cancel2()
		if err != nil {
			c.log("debug", "ws", fmt.Sprintf("reconnect dial #%d failed: %v", attempt+1, err))
			time.Sleep(d)
			continue
		}
		c.log("info", "ws", fmt.Sprintf("reconnected (attempt %d)", attempt+1))
		c.flushOutbox()
		return
	}
	c.log("error", "ws", "all reconnect attempts failed")
}

func (c *Client) flushOutbox() {
	c.mu.Lock()
	ob := c.outbox
	c.outbox = nil
	ws := c.ws
	connected := c.connected
	c.mu.Unlock()
	if ws == nil || !connected {
		c.mu.Lock()
		c.outbox = append(ob, c.outbox...)
		c.mu.Unlock()
		return
	}
	for i, msg := range ob {
		if err := ws.WriteMessage(websocket.TextMessage, msg); err != nil {
			// Push unsent back.
			c.mu.Lock()
			c.outbox = append(ob[i:], c.outbox...)
			c.mu.Unlock()
			return
		}
	}
}

// log routes through the Logger hook if set, else to stderr for high levels.
func (c *Client) log(level, tag, msg string) {
	if c.Logger != nil {
		c.Logger(level, tag, msg)
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
