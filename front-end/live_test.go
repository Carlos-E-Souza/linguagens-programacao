package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/api"
	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/cache"
	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/events"
	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/queue"
)

type emitted struct {
	name string
	data any
}

type emittedEvents struct {
	mu     sync.Mutex
	values []emitted
}

func (e *emittedEvents) snapshot() []emitted {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]emitted(nil), e.values...)
}

// liveSession monta uma sessão sem WebSocket (os envios viram no-op) com uma conversa entre os usuários 1 e 2. O
// REST responde com as mensagens 1..serverLast da conversa, todas do usuário 2.
func liveSession(t *testing.T) (*App, *session, *emittedEvents) {
	return liveSessionWithServer(t, 0)
}

func liveSessionWithServer(t *testing.T, serverLast int64) (*App, *session, *emittedEvents) {
	t.Helper()
	ctx := context.Background()
	c, err := cache.Open(filepath.Join(t.TempDir(), "cliente.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	me := api.User{ID: 1, Username: "ana"}
	if err := c.SaveSession(ctx, "token", me); err != nil {
		t.Fatal(err)
	}
	if err := c.SaveConversations(ctx, []api.Conversation{{ID: 4, Type: api.ConversationDirect,
		Members: []api.Member{{UserID: 1}, {UserID: 2}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SyncCursor(ctx, 4); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/conversations" {
			conversations, err := c.ListConversations(ctx)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(conversations)
			return
		}
		before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
		page := []api.Message{}
		for seq := int64(1); seq <= serverLast; seq++ {
			if before == 0 || seq < before {
				page = append(page, api.Message{ID: 10 + seq, ConversationID: 4, Sequence: seq, SenderID: 2,
					ClientID: "m" + strconv.FormatInt(seq, 10)})
			}
		}
		json.NewEncoder(w).Encode(page)
	}))
	t.Cleanup(server.Close)
	s := &session{user: me, cache: c, ctx: ctx}
	s.queue = queue.New(c, func(api.Message) error { return nil }, queue.Options{})
	got := &emittedEvents{}
	a := &App{api: api.NewClient(server.URL), session: s, emit: func(name string, data ...any) {
		var payload any
		if len(data) > 0 {
			payload = data[0]
		}
		got.mu.Lock()
		got.values = append(got.values, emitted{name, payload})
		got.mu.Unlock()
	}}
	return a, s, got
}

// waitFor espera cond ficar verdadeira (usada com o trabalho feito em goroutines).
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condição não foi atingida a tempo")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func raw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestHandleMessageNewSavesCountsAndAdvancesCursor(t *testing.T) {
	a, s, got := liveSession(t)
	msg := api.Message{ID: 10, ConversationID: 4, Sequence: 1, SenderID: 2, ClientID: "x", Body: "oi"}
	a.handleMessageNew(s, raw(t, msg))
	a.handleMessageNew(s, raw(t, msg)) // repetida: não conta duas vezes

	conv, _, _ := s.cache.GetConversation(s.ctx, 4)
	if conv.UnreadCount != 1 || conv.LastMessage == nil || conv.LastMessage.Body != "oi" {
		t.Fatalf("conversa = %+v", conv)
	}
	if cursor, _ := s.cache.SyncCursor(s.ctx, 4); cursor != 1 {
		t.Fatalf("cursor = %d", cursor)
	}
	recorded := got.snapshot()
	if len(recorded) != 4 || recorded[0].name != events.MessageNew || recorded[1].name != events.ConversationUpdated {
		t.Fatalf("eventos = %+v", recorded)
	}

}

func TestHandleMessageNewFillsGapThroughREST(t *testing.T) {
	a, s, _ := liveSessionWithServer(t, 3)
	a.handleMessageNew(s, raw(t, api.Message{ID: 11, ConversationID: 4, Sequence: 1, SenderID: 2, ClientID: "m1"}))
	// A 2 se perdeu: a 3 dispara a busca do trecho pelo REST.
	a.handleMessageNew(s, raw(t, api.Message{ID: 13, ConversationID: 4, Sequence: 3, SenderID: 2, ClientID: "m3"}))
	waitFor(t, func() bool {
		cursor, _ := s.cache.SyncCursor(s.ctx, 4)
		return cursor == 3
	})
	messages, _ := s.cache.ListMessages(s.ctx, 4, 0, 10)
	conv, _, _ := s.cache.GetConversation(s.ctx, 4)
	if len(messages) != 3 || conv.UnreadCount != 3 {
		t.Fatalf("mensagens = %d, não lidas = %d", len(messages), conv.UnreadCount)
	}
}

func TestSyncFinishesOnlyForItsGeneration(t *testing.T) {
	a, s, _ := liveSession(t)
	s.syncActive, s.syncGeneration = true, 2
	a.finishSyncGeneration(s, 1) // prazo de uma sincronização antiga
	if !s.syncActive {
		t.Fatal("prazo antigo encerrou a sincronização atual")
	}
	a.finishSyncGeneration(s, 2)
	if s.syncActive {
		t.Fatal("prazo não encerrou a sincronização")
	}
}

func TestServerErrorKeepsTransientFailuresQueued(t *testing.T) {
	a, s, got := liveSession(t)
	for _, id := range []string{"a", "b"} {
		if err := s.queue.Add(s.ctx, api.Message{ConversationID: 4, SenderID: 1, ClientID: id, Body: "oi"}); err != nil {
			t.Fatal(err)
		}
	}
	a.handleServerError(s, raw(t, map[string]string{"code": "internal_error", "message": "x", "client_id": "a"}))
	a.handleServerError(s, raw(t, map[string]string{"code": "not_a_member", "message": "you are not a member",
		"client_id": "b"}))
	pending, _ := s.cache.PendingSends(s.ctx)
	if len(pending) != 1 || pending[0].ClientID != "a" {
		t.Fatalf("fila = %+v", pending)
	}
	recorded := got.snapshot()
	if len(recorded) != 1 {
		t.Fatalf("eventos = %+v", recorded)
	}
	update := recorded[0].data.(events.MessageUpdatedPayload)
	if update.ClientID != "b" || update.Status != api.StatusFailed || update.Error != "Você não participa mais desta conversa." {
		t.Fatalf("message:updated = %+v", update)
	}
}

func TestHandleMessageNewDuringSyncIsBuffered(t *testing.T) {
	a, s, got := liveSession(t)
	s.syncActive = true
	a.handleMessageNew(s, raw(t, api.Message{ID: 10, ConversationID: 4, Sequence: 1, SenderID: 2, ClientID: "x"}))
	recorded := got.snapshot()
	if len(s.syncMessages) != 1 || len(recorded) != 0 {
		t.Fatalf("buffer = %d, eventos = %+v", len(s.syncMessages), recorded)
	}
}

func TestHandleMessageStatusEmitsConversationPosition(t *testing.T) {
	a, s, got := liveSession(t)
	if err := s.cache.SaveMessages(s.ctx, []api.Message{
		{ID: 10, ConversationID: 4, Sequence: 1, SenderID: 1, ClientID: "a"},
		{ID: 11, ConversationID: 4, Sequence: 2, SenderID: 1, ClientID: "b"},
	}); err != nil {
		t.Fatal(err)
	}
	a.handleMessageStatus(s, raw(t, events.MessageStatusPayload{ConversationID: 4, UserID: 2, UpToSequence: 1,
		Status: api.StatusRead}))
	recorded := got.snapshot()
	if len(recorded) != 3 {
		t.Fatalf("eventos = %+v", recorded)
	}
	read := recorded[1].data.(events.MessageStatusPayload)
	if read.Status != api.StatusRead || read.UpToSequence != 1 {
		t.Fatalf("message:status = %+v", read)
	}
	msg, _, _ := s.cache.GetMessage(s.ctx, "a")
	if msg.Status != api.StatusRead {
		t.Fatalf("status = %s", msg.Status)
	}
}

func TestMarkAsReadClearsUnread(t *testing.T) {
	a, s, _ := liveSession(t)
	a.handleMessageNew(s, raw(t, api.Message{ID: 10, ConversationID: 4, Sequence: 1, SenderID: 2, ClientID: "x"}))
	if err := a.MarkAsRead(4, 1); err != nil {
		t.Fatal(err)
	}
	conv, _, _ := s.cache.GetConversation(s.ctx, 4)
	if conv.UnreadCount != 0 {
		t.Fatalf("não lidas = %d", conv.UnreadCount)
	}
}

func TestConversationUpdatedAddsAndRemoves(t *testing.T) {
	a, s, got := liveSession(t)
	group := api.Conversation{ID: 9, Type: api.ConversationGroup, Name: "LP",
		Members: []api.Member{{UserID: 1}, {UserID: 2, Role: "admin"}}}
	a.handleConversationUpdated(s, raw(t, map[string]any{"conversation": group}))
	if _, ok, _ := s.cache.GetConversation(s.ctx, 9); !ok {
		t.Fatal("grupo novo não entrou no cache")
	}
	recorded := got.snapshot()
	if len(recorded) != 1 || recorded[0].name != events.ConversationUpdated {
		t.Fatalf("eventos = %+v", recorded)
	}

	// Sem o usuário entre os membros: ele foi removido.
	group.Members = []api.Member{{UserID: 2, Role: "admin"}}
	a.handleConversationUpdated(s, raw(t, map[string]any{"conversation": group}))
	if _, ok, _ := s.cache.GetConversation(s.ctx, 9); ok {
		t.Fatal("grupo continua no cache depois da remoção")
	}
	recorded = got.snapshot()
	last := recorded[len(recorded)-1]
	if last.name != events.ConversationRemoved {
		t.Fatalf("último evento = %+v", last)
	}
}
