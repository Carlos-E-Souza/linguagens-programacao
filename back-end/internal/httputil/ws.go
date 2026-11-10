package httputil

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/auth"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/hub"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/ws"
)

// maxFrameBytes bounds a single incoming WebSocket frame. A client that
// sends more than this is either broken or hostile; either way the
// connection is closed rather than buffering it.
const maxFrameBytes = 1 << 20 // 1 MiB

// writeTimeout bounds one frame write, so a peer that stops reading
// cannot pin a goroutine forever.
const writeTimeout = 10 * time.Second

// handleWS upgrades an authenticated request and hands the connection to
// the hub.
//
// Authentication happens before the upgrade, using the same bearer token
// as the REST API. The client is Go, so it can set the header on the
// handshake; that avoids a query-string token, which would end up in
// server logs and browser history.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if s.hub == nil {
		WriteError(w, http.StatusServiceUnavailable, "not_configured", "the realtime hub is not configured")
		return
	}
	if s.validator == nil {
		WriteError(w, http.StatusServiceUnavailable, "not_configured", "authentication is not configured")
		return
	}

	raw, err := auth.BearerToken(r.Header.Get("Authorization"))
	if err != nil {
		WriteError(w, http.StatusUnauthorized, "unauthorized", "missing or malformed authorization header")
		return
	}

	userID, err := s.validator.ValidateToken(raw)
	if err != nil {
		WriteError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired token")
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// The desktop client is a Wails/Go process, not a browser page, so
		// it sends no Origin header. The default check would reject it.
		InsecureSkipVerify: true,
	})
	if err != nil {
		slog.Debug("websocket accept failed", "user_id", userID, "error", err)
		return
	}

	conn.SetReadLimit(maxFrameBytes)

	s.serveConnection(conn, userID)
}

// serveConnection runs the read and write loops until either fails.
//
// Both loops share one context, so when either ends the other is
// cancelled: a reader that fails must not leave a writer blocked on a
// dead socket, and vice versa.
func (s *Server) serveConnection(conn *websocket.Conn, userID int64) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := s.hub.NewClient(userID, conn)
	s.hub.Register(client)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		defer cancel()
		s.writeLoop(ctx, client)
	}()

	go func() {
		defer wg.Done()
		defer cancel()
		s.readLoop(ctx, client)
	}()

	// Wait for both loops to end, then make sure the hub forgets the
	// client. Unregister is idempotent, so a double call is harmless.
	wg.Wait()

	s.hub.Unregister(client)
	_ = conn.Close(websocket.StatusNormalClosure, "connection closed")
}

// readLoop decodes frames and hands them to the hub.
//
// It never touches hub state directly: every frame goes through the
// hub's incoming channel, which is what keeps the hub single-threaded.
func (s *Server) readLoop(ctx context.Context, client *hub.Client) {
	for {
		msgType, data, err := client.Read(ctx)
		if err != nil {
			slog.Debug("websocket read ended", "user_id", client.UserID(), "error", err)
			return
		}
		if msgType != websocket.MessageText {
			continue
		}

		var env ws.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			slog.Debug("discarding malformed frame", "user_id", client.UserID(), "error", err)
			continue
		}
		if env.Type == "" {
			slog.Debug("discarding frame without a type", "user_id", client.UserID())
			continue
		}

		if err := s.hub.Submit(ctx, client, env, len(data)); err != nil {
			return
		}
	}
}

// writeLoop drains the outgoing queue and writes each frame.
//
// A single writer per connection is what makes the WebSocket safe:
// coder/websocket allows only one concurrent writer, and serialising here
// means the hub can hand frames over without coordinating.
func (s *Server) writeLoop(ctx context.Context, client *hub.Client) {
	for {
		frame, ok := client.Next(ctx)
		if !ok {
			return
		}

		writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
		err := client.Write(writeCtx, frame)
		cancel()

		if err != nil {
			slog.Debug("websocket write ended", "user_id", client.UserID(), "error", err)
			return
		}
	}
}
