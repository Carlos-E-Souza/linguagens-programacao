package hub

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Client is one open WebSocket connection belonging to one user.
//
// A user may have several connections at once (two windows, a phone and a
// laptop), which is why the hub keys them by user ID and keeps a set per
// user rather than a single entry.
type Client struct {
	// userID and conn are set once at construction and only read
	// afterwards, so they need no synchronisation.
	userID int64
	conn   *websocket.Conn

	// send is the outgoing queue. The hub writes to it; the write
	// goroutine drains it. It is buffered so a momentary slowdown on the
	// socket does not stall the hub loop.
	send chan []byte

	// cancel tears down both goroutines of the connection. Calling it is
	// how any part of the system asks the connection to stop.
	cancel context.CancelFunc

	// ctx is the connection's own context, cancelled by cancel above. It
	// is stored so the hub can check whether the connection is already
	// gone before blocking on a channel send.
	ctx context.Context

	// closeOnce guards close(send): more than one path may decide the
	// client is finished, and closing a channel twice panics.
	closeOnce sync.Once

	// typingMu guards lastTyping, which is per connection because the
	// cooldown is about what the user is currently doing.
	typingMu   sync.Mutex
	lastTyping time.Time
}

// newClient builds a client with its own cancelable context.
//
// The returned context is intentionally not derived from the request
// context: the HTTP request ends as soon as the handler returns, while
// the connection must outlive it.
func newClient(userID int64, conn *websocket.Conn, queueSize int) (*Client, context.Context) {
	ctx, cancel := context.WithCancel(context.Background())

	c := &Client{
		userID: userID,
		conn:   conn,
		send:   make(chan []byte, queueSize),
		cancel: cancel,
		ctx:    ctx,
	}

	return c, ctx
}

// context returns the connection's own context.
func (c *Client) context() context.Context {
	return c.ctx
}

// UserID returns the owner of this connection.
func (c *Client) UserID() int64 {
	return c.userID
}

// trySend queues a frame without blocking.
//
// It reports false when the queue is full, which the hub treats as a slow
// client: dropping the connection is better than letting one stalled
// consumer block delivery for everyone else.
func (c *Client) trySend(frame []byte) bool {
	select {
	case c.send <- frame:
		return true
	default:
		return false
	}
}

// close releases the client's resources. It is safe to call more than
// once, which matters because several goroutines may decide the
// connection is over at the same time.
func (c *Client) close() {
	c.closeOnce.Do(func() {
		close(c.send)
		c.cancel()
	})
}

// allowTyping applies the per-user cooldown and reports whether a typing
// event should be forwarded.
//
// Without this, a client could emit an event on every keystroke and turn
// typing indicators into a broadcast flood.
func (c *Client) allowTyping(cooldown time.Duration) bool {
	if cooldown <= 0 {
		return true
	}

	c.typingMu.Lock()
	defer c.typingMu.Unlock()

	now := time.Now()
	if now.Sub(c.lastTyping) < cooldown {
		return false
	}
	c.lastTyping = now

	return true
}

// encodeFrame marshals an envelope into the bytes the writer sends.
func encodeFrame(env any) ([]byte, error) {
	return json.Marshal(env)
}

// logClientError records a connection-scoped failure with its user.
func logClientError(userID int64, msg string, err error) {
	slog.Debug(msg, "user_id", userID, "error", err)
}

// Read reads one frame from the connection.
//
// Only the read goroutine calls this; coder/websocket allows one reader
// at a time.
func (c *Client) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	return c.conn.Read(ctx)
}

// Write sends one frame.
//
// Only the write goroutine calls this, which is what keeps the
// single-writer rule of coder/websocket satisfied without a mutex.
func (c *Client) Write(ctx context.Context, frame []byte) error {
	return c.conn.Write(ctx, websocket.MessageText, frame)
}

// Next returns the next queued outgoing frame, blocking until one is
// available or the connection is closed.
//
// The second return value is false once the queue is closed and drained,
// which is how the write goroutine learns to stop. Draining rather than
// aborting matters: frames already queued (a final error, a shutdown
// notice) still reach the client.
func (c *Client) Next(ctx context.Context) ([]byte, bool) {
	select {
	case frame, ok := <-c.send:
		return frame, ok
	case <-ctx.Done():
		return nil, false
	}
}
