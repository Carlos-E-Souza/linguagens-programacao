package httputil

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/coder/websocket"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/hub"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/ws"
)

// stubHub records the notifications the REST layer pushes into it, and
// reports a fixed online state, so handlers can be tested without a real
// hub or a real socket.
type stubHub struct {
	mu            sync.Mutex
	notifications []hub.Notification
	online        map[int64]bool
}

func newStubHub() *stubHub {
	return &stubHub{online: make(map[int64]bool)}
}

func (s *stubHub) Notify(ctx context.Context, n hub.Notification) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifications = append(s.notifications, n)
}

func (s *stubHub) Notifications() []hub.Notification {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]hub.Notification, len(s.notifications))
	copy(out, s.notifications)
	return out
}

func (s *stubHub) IsOnline(userID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.online[userID]
}

func (s *stubHub) SetOnline(userID int64, online bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.online[userID] = online
}

// The remaining methods satisfy RealtimeHub but are unused here: these
// tests exercise the REST side only.
func (s *stubHub) NewClient(userID int64, conn *websocket.Conn) *hub.Client { return nil }
func (s *stubHub) Register(client *hub.Client)                              {}
func (s *stubHub) Unregister(client *hub.Client)                            {}
func (s *stubHub) Submit(ctx context.Context, client *hub.Client, env ws.Envelope, rawSize int) error {
	return nil
}

// TestCreateGroupNotifiesHub checks the REST layer tells the hub about a
// new conversation, which is what makes connected clients refresh.
func TestCreateGroupNotifiesHub(t *testing.T) {
	f := newFixture(t)
	hubStub := newStubHub()
	f.server.SetHub(hubStub)

	f.register("alice")
	bob := f.register("bob")
	token := f.login("alice")

	rec := f.do(http.MethodPost, "/api/conversations", token, createConversationRequest{
		Type: models.ConversationGroup, Name: "team", Members: []int64{bob.ID},
	})
	if got, want := rec.Code, http.StatusCreated; got != want {
		t.Fatalf("status: got %d, want %d (%s)", got, want, rec.Body)
	}

	notifications := hubStub.Notifications()
	if got, want := len(notifications), 1; got != want {
		t.Fatalf("notifications: got %d, want %d", got, want)
	}
	if got, want := notifications[0].Type, hub.NotifyConversationUpdated; got != want {
		t.Errorf("notification type: got %q, want %q", got, want)
	}
	if notifications[0].ConversationID == 0 {
		t.Error("notification has no conversation id")
	}
}

// TestRemoveMemberNotifiesHub covers the other membership mutation.
func TestRemoveMemberNotifiesHub(t *testing.T) {
	f := newFixture(t)
	hubStub := newStubHub()
	f.server.SetHub(hubStub)

	f.register("alice")
	bob := f.register("bob")
	token := f.login("alice")

	rec := f.do(http.MethodPost, "/api/conversations", token, createConversationRequest{
		Type: models.ConversationGroup, Name: "team", Members: []int64{bob.ID},
	})
	var conv models.Conversation
	_ = json.Unmarshal(rec.Body.Bytes(), &conv)

	before := len(hubStub.Notifications())

	rec = f.do(http.MethodDelete,
		"/api/conversations/"+itoa(conv.ID)+"/members/"+itoa(bob.ID), token, nil)
	if got, want := rec.Code, http.StatusOK; got != want {
		t.Fatalf("status: got %d, want %d (%s)", got, want, rec.Body)
	}

	if got, want := len(hubStub.Notifications()), before+1; got != want {
		t.Errorf("notifications after removal: got %d, want %d", got, want)
	}
}

// TestPresenceReflectedInSearch checks the online flag reaches the client.
func TestPresenceReflectedInSearch(t *testing.T) {
	f := newFixture(t)
	hubStub := newStubHub()
	f.server.SetHub(hubStub)

	f.register("alice")
	bob := f.register("bob")
	token := f.login("alice")

	hubStub.SetOnline(bob.ID, true)

	rec := f.do(http.MethodGet, "/api/users?search=bob", token, nil)
	if got, want := rec.Code, http.StatusOK; got != want {
		t.Fatalf("status: got %d, want %d", got, want)
	}

	var results []userResult
	if err := json.Unmarshal(rec.Body.Bytes(), &results); err != nil {
		t.Fatalf("decode results: %v", err)
	}
	if got, want := len(results), 1; got != want {
		t.Fatalf("result count: got %d, want %d", got, want)
	}
	if !results[0].Online {
		t.Error("expected the user to be reported online")
	}
}

// TestWebSocketRequiresToken covers the handshake authentication: an
// unauthenticated upgrade must be refused before the connection is
// established.
func TestWebSocketRequiresToken(t *testing.T) {
	f := newFixture(t)
	f.server.SetHub(newStubHub())

	cases := []struct {
		name   string
		header string
	}{
		{"no header", ""},
		{"malformed scheme", "Basic abc"},
		{"invalid token", "Bearer not-a-real-token"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/ws", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}

			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, req)

			if got, want := rec.Code, http.StatusUnauthorized; got != want {
				t.Errorf("status: got %d, want %d", got, want)
			}
		})
	}
}

// TestWebSocketUnavailableWithoutHub documents the 503 path, which is
// what a misconfigured deployment would see instead of a panic.
func TestWebSocketUnavailableWithoutHub(t *testing.T) {
	f := newFixture(t)

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Authorization", "Bearer "+f.login(f.register("alice").Username))

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusServiceUnavailable; got != want {
		t.Errorf("status: got %d, want %d (%s)", got, want, rec.Body)
	}
}
