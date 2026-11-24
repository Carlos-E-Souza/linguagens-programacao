package httputil

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/auth"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/hub"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/ws"
	"golang.org/x/crypto/bcrypt"
)

// realtimeFixture is a real HTTP server with a running hub, reached over
// actual WebSocket connections.
type realtimeFixture struct {
	*apiFixture
	hub *hub.Hub
	url string

	// stopHub cancels the hub and waits for it; safe to call twice.
	stopHub func()
}

// newRealtimeFixture starts the server with short heartbeat intervals so
// dead connections are detected quickly.
func newRealtimeFixture(t *testing.T, pingInterval, pongTimeout time.Duration) *realtimeFixture {
	t.Helper()

	base := newFixture(t)

	cfg := base.server.cfg
	cfg.PingInterval = pingInterval
	cfg.PongTimeout = pongTimeout
	cfg.ClientQueueSize = 64

	realtime := hub.New(cfg, base.store, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		realtime.Run(ctx)
	}()

	server := NewServer(cfg, base.store, auth.NewHasher(bcrypt.MinCost), base.issuer)
	server.SetHub(realtime)

	httpServer := httptest.NewServer(server.Routes())

	var once sync.Once
	stopHub := func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}

	t.Cleanup(func() {
		stopHub()
		httpServer.Close()
	})

	return &realtimeFixture{
		apiFixture: base,
		hub:        realtime,
		url:        "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws",
		stopHub:    stopHub,
	}
}

// dial opens an authenticated WebSocket connection.
func (f *realtimeFixture) dial(token string) *websocket.Conn {
	f.t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, f.url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		f.t.Fatalf("websocket.Dial() failed: %v", err)
	}
	return conn
}

// waitOnline polls the hub until the user's presence matches want.
func (f *realtimeFixture) waitOnline(userID int64, want bool, timeout time.Duration) {
	f.t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if f.hub.IsOnline(userID) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.t.Fatalf("user %d online=%v not reached within %s", userID, want, timeout)
}

// readPresence reads frames until a presence event about userID arrives.
func readPresence(t *testing.T, conn *websocket.Conn, userID int64, timeout time.Duration) ws.PresencePayload {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("no presence for user %d: %v", userID, err)
		}

		var env ws.Envelope
		if err := json.Unmarshal(data, &env); err != nil || env.Type != ws.TypePresence {
			continue
		}
		var p ws.PresencePayload
		if err := json.Unmarshal(env.Data, &p); err != nil {
			t.Fatalf("decode presence: %v", err)
		}
		if p.UserID == userID {
			return p
		}
	}
}

// seedPair registers alice and bob and opens a direct conversation, so
// each is the other's contact.
func (f *realtimeFixture) seedPair() (alice, bob models.User, aliceToken, bobToken string) {
	f.t.Helper()

	alice = f.register("alice")
	bob = f.register("bob")
	aliceToken = f.login("alice")
	bobToken = f.login("bob")

	rec := f.do(http.MethodPost, "/api/conversations", aliceToken, createConversationRequest{
		Type: models.ConversationDirect, Members: []int64{bob.ID},
	})
	if rec.Code != http.StatusCreated {
		f.t.Fatalf("create conversation: status %d, body %s", rec.Code, rec.Body)
	}
	return alice, bob, aliceToken, bobToken
}

// TestWebSocketTwoClientsSeePresence covers Phase 4 over real sockets: a
// contact learns when the other connects and when they disconnect.
func TestWebSocketTwoClientsSeePresence(t *testing.T) {
	f := newRealtimeFixture(t, time.Minute, 2*time.Minute)
	alice, bob, aliceToken, bobToken := f.seedPair()

	bobConn := f.dial(bobToken)
	defer bobConn.CloseNow()
	f.waitOnline(bob.ID, true, time.Second)

	aliceConn := f.dial(aliceToken)
	if p := readPresence(t, bobConn, alice.ID, 2*time.Second); !p.Online {
		t.Error("bob should see alice online after she connects")
	}

	aliceConn.Close(websocket.StatusNormalClosure, "bye")
	if p := readPresence(t, bobConn, alice.ID, 2*time.Second); p.Online {
		t.Error("bob should see alice offline after she disconnects")
	}
}

// TestUnresponsiveClientIsRemoved covers Phase 9: a connection that stops
// answering pings is dropped from the hub and its contacts are told.
//
// coder/websocket only answers a ping while the connection is being read,
// so a client that never calls Read is exactly a peer that went silent.
func TestUnresponsiveClientIsRemoved(t *testing.T) {
	f := newRealtimeFixture(t, 100*time.Millisecond, 500*time.Millisecond)
	alice, bob, aliceToken, bobToken := f.seedPair()

	bobConn := f.dial(bobToken)
	defer bobConn.CloseNow()
	f.waitOnline(bob.ID, true, time.Second)

	// Bob starts reading before Alice connects, so his pings are answered
	// while we wait for the silent client to time out.
	silent := f.dial(aliceToken) // never read: will not answer pings
	defer silent.CloseNow()
	f.waitOnline(alice.ID, true, time.Second)
	if p := readPresence(t, bobConn, alice.ID, 2*time.Second); !p.Online {
		t.Error("expected an online presence when the silent client connects")
	}

	if p := readPresence(t, bobConn, alice.ID, 2*time.Second); p.Online {
		t.Error("expected an offline presence for the silent client")
	}
	f.waitOnline(alice.ID, false, time.Second)

	if !f.hub.IsOnline(bob.ID) {
		t.Error("the responsive client must stay connected")
	}
}

// TestConnectionsDoNotLeakGoroutines covers the Phase 9 leak check:
// repeated connect/disconnect cycles leave the goroutine count where it
// started.
func TestConnectionsDoNotLeakGoroutines(t *testing.T) {
	f := newRealtimeFixture(t, time.Minute, 2*time.Minute)
	alice := f.register("alice")
	token := f.login("alice")

	cycle := func() {
		conn := f.dial(token)
		f.waitOnline(alice.ID, true, time.Second)
		conn.Close(websocket.StatusNormalClosure, "cycle")
		f.waitOnline(alice.ID, false, time.Second)
	}

	// One warm-up cycle lets the HTTP client and server settle their
	// long-lived goroutines before the baseline is taken.
	cycle()
	time.Sleep(100 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	for i := 0; i < 30; i++ {
		cycle()
	}

	// Goroutines finish asynchronously after the close handshake.
	const tolerance = 3
	deadline := time.Now().Add(3 * time.Second)
	for {
		now := runtime.NumGoroutine()
		if now <= baseline+tolerance {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines leaked: baseline %d, after 30 cycles %d", baseline, now)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestShutdownClosesSocketsWithGoingAway covers Phase 10 over a real
// socket: the client gets server.shutdown and then a GoingAway close.
func TestShutdownClosesSocketsWithGoingAway(t *testing.T) {
	f := newRealtimeFixture(t, time.Minute, 2*time.Minute)
	alice := f.register("alice")
	conn := f.dial(f.login("alice"))
	defer conn.CloseNow()
	f.waitOnline(alice.ID, true, time.Second)

	f.stopHub()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	sawShutdown := false
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if got := websocket.CloseStatus(err); got != websocket.StatusGoingAway {
				t.Errorf("close status: got %v, want %v (err: %v)", got, websocket.StatusGoingAway, err)
			}
			break
		}
		var env ws.Envelope
		if json.Unmarshal(data, &env) == nil && env.Type == ws.TypeServerShutdown {
			sawShutdown = true
		}
	}
	if !sawShutdown {
		t.Error("the client did not receive server.shutdown before the close")
	}
}
