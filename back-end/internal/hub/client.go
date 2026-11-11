package hub

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

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

	// sendMu guards the closed flag and the send itself. Without it, a
	// trySend running concurrently with close would send on a closed
	// channel, which panics. Closing the channel is what tells the write
	// goroutine to drain and stop, so it cannot simply be left open.
	sendMu sync.Mutex
	closed bool

	// closeCode and closeReason are what the connection should be closed
	// with, chosen by whoever closed the client first. Guarded by sendMu.
	closeCode   websocket.StatusCode
	closeReason string
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
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	if c.closed {
		return false
	}

	select {
	case c.send <- frame:
		return true
	default:
		return false
	}
}

// close releases the client's resources with a normal close status. It is
// safe to call more than once, which matters because several goroutines
// may decide the connection is over at the same time.
func (c *Client) close() {
	c.closeWith(websocket.StatusNormalClosure, "connection closed")
}

// closeWith releases the client's resources and records the WebSocket
// status the connection should be closed with. Only the first call wins:
// a slow client closed with StatusPolicyViolation keeps that code even
// though the regular unregister path calls close afterwards.
func (c *Client) closeWith(code websocket.StatusCode, reason string) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	if c.closed {
		return
	}
	c.closed = true
	c.closeCode = code
	c.closeReason = reason

	// Cancelling before closing lets anything blocked on the context give
	// up; the close then lets the write goroutine drain and exit.
	c.cancel()
	close(c.send)
}

// CloseStatus returns the status and reason the connection should be
// closed with. A client that was never closed by the hub reports a normal
// closure.
func (c *Client) CloseStatus() (websocket.StatusCode, string) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	if !c.closed {
		return websocket.StatusNormalClosure, "connection closed"
	}
	return c.closeCode, c.closeReason
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

// Ping sends a WebSocket ping and waits for the matching pong.
//
// It is called only from the connection's write goroutine, so it does not
// race with ordinary frame writes: coder/websocket permits a single
// concurrent writer, and the heartbeat and the outgoing queue share that
// one goroutine.
//
// A failed ping is how a half-open connection is detected. TCP alone will
// not notice a peer that vanished without closing (a laptop that slept, a
// network that dropped), so without this the connection and its two
// goroutines would linger indefinitely.
func (c *Client) Ping(ctx context.Context) error {
	return c.conn.Ping(ctx)
}
