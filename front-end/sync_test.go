package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/api"
	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/cache"
	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/events"
	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/queue"
)

func TestFetchMessagesSinceUsesPagesAndFindsGaps(t *testing.T) {
	missing := int64(0)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
		var page []api.Message
		for seq := int64(625); seq >= 1 && len(page) < pageSize; seq-- {
			if seq == missing || (before > 0 && seq >= before) {
				continue
			}
			page = append(page, api.Message{ID: seq, ConversationID: 1, Sequence: seq,
				SenderID: 2, ClientID: strconv.FormatInt(seq, 10)})
		}
		for i, j := 0, len(page)-1; i < j; i, j = i+1, j-1 {
			page[i], page[j] = page[j], page[i]
		}
		json.NewEncoder(w).Encode(page)
	}))
	defer server.Close()
	a := &App{api: api.NewClient(server.URL)}
	s := &session{ctx: context.Background()}

	got, err := a.fetchMessagesSince(s, 1, 2, 625)
	if err != nil || len(got) != 623 || got[0].Sequence != 3 || got[len(got)-1].Sequence != 625 {
		t.Fatalf("histórico = %d mensagens, erro = %v", len(got), err)
	}
	if requests < 13 {
		t.Fatalf("paginação usou só %d requisições", requests)
	}

	missing = 561
	_, err = a.fetchMessagesSince(s, 1, 2, 625)
	if err == nil || !strings.Contains(err.Error(), "561") {
		t.Fatalf("esperava detectar a sequência 561 ausente; erro = %v", err)
	}
}

func TestCompleteSyncStoresOfflineMessagesAndAdvancesCursor(t *testing.T) {
	ctx := context.Background()
	c, err := cache.Open(filepath.Join(t.TempDir(), "cliente.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	first := api.Message{ID: 1, ConversationID: 1, Sequence: 1, SenderID: 2, ClientID: "1", Body: "um"}
	if err := c.SaveConversations(ctx, []api.Conversation{{ID: 1, Type: api.ConversationDirect,
		LastSequence: 1, LastMessage: &first}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SyncCursor(ctx, 1); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/conversations" {
			json.NewEncoder(w).Encode([]api.Conversation{{ID: 1, Type: api.ConversationDirect, LastSequence: 3}})
			return
		}
		json.NewEncoder(w).Encode([]api.Message{
			first,
			{ID: 2, ConversationID: 1, Sequence: 2, SenderID: 2, ClientID: "2", Body: "dois"},
			{ID: 3, ConversationID: 1, Sequence: 3, SenderID: 2, ClientID: "3", Body: "tres"},
		})
	}))
	defer server.Close()
	s := &session{ctx: ctx, cache: c, syncGeneration: 1}
	s.queue = queue.New(c, func(api.Message) error { return nil }, queue.Options{})
	var synced int
	a := &App{api: api.NewClient(server.URL), session: s, emit: func(name string, data ...any) {
		if name == events.MessagesSynced {
			synced = len(data[0].(events.MessagesSyncedPayload).Messages)
		}
	}}
	a.completeSync(s, 1, map[int64]int64{1: 1}, []api.Message{{
		ID: 2, ConversationID: 1, Sequence: 2, SenderID: 2, ClientID: "2", Body: "dois",
	}})
	got, err := c.ListMessages(ctx, 1, 0, 10)
	if err != nil || len(got) != 3 || synced != 2 {
		t.Fatalf("mensagens = %+v, evento = %d, erro = %v", got, synced, err)
	}
	cursor, err := c.SyncCursor(ctx, 1)
	if err != nil || cursor != 3 {
		t.Fatalf("cursor = %d, erro = %v", cursor, err)
	}
}

func TestCompleteSyncRetriesStaleListBeforeAdvancingCursor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := cache.Open(filepath.Join(t.TempDir(), "cliente.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	first := api.Message{ID: 1, ConversationID: 1, Sequence: 1, SenderID: 2, ClientID: "1", Body: "um"}
	base := api.Conversation{ID: 1, Type: api.ConversationDirect, LastSequence: 1,
		LastMessage: &first, Members: []api.Member{{UserID: 1}, {UserID: 2}}}
	if err := c.SaveConversations(ctx, []api.Conversation{base}); err != nil {
		t.Fatal(err)
	}
	if err := c.SaveSyncCursor(ctx, 1, 1); err != nil {
		t.Fatal(err)
	}
	remoteMessage := api.Message{ID: 4, ConversationID: 2, Sequence: 1,
		SenderID: 3, ClientID: "4", Body: "novo"}
	remote := api.Conversation{ID: 2, Type: api.ConversationDirect, LastSequence: 1,
		UnreadCount: 1, LastMessage: &remoteMessage,
		Members: []api.Member{{UserID: 1}, {UserID: 3, Online: true}}}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/conversations" {
			if calls.Add(1) <= 3 {
				// Um evento WebSocket durante cada GET torna a resposta velha.
				c.SetPresence(ctx, 2, true)
			}
			fresh := base
			fresh.LastSequence = 3
			fresh.UnreadCount = 2
			fresh.Members = []api.Member{{UserID: 1}, {UserID: 2, Online: true}}
			json.NewEncoder(w).Encode([]api.Conversation{fresh, remote})
			return
		}
		if r.URL.Path == "/api/conversations/2/messages" {
			json.NewEncoder(w).Encode([]api.Message{remoteMessage})
			return
		}
		json.NewEncoder(w).Encode([]api.Message{
			first,
			{ID: 2, ConversationID: 1, Sequence: 2, SenderID: 2, ClientID: "2", Body: "dois"},
			{ID: 3, ConversationID: 1, Sequence: 3, SenderID: 2, ClientID: "3", Body: "tres"},
		})
	}))
	t.Cleanup(server.Close)
	s := &session{ctx: ctx, cache: c, user: api.User{ID: 1}, syncGeneration: 1}
	s.queue = queue.New(c, func(api.Message) error { return nil }, queue.Options{})
	a := &App{api: api.NewClient(server.URL), session: s, emit: func(string, ...any) {}}
	a.completeSync(s, 1, map[int64]int64{1: 1}, nil)
	cursor, err := c.SyncCursor(ctx, 1)
	if err != nil || cursor != 1 || calls.Load() != 3 {
		t.Fatalf("antes da nova tentativa: cursor=%d buscas=%d erro=%v", cursor, calls.Load(), err)
	}
	if _, exists, err := c.GetConversation(ctx, 2); err != nil || exists {
		t.Fatalf("conversa remota aplicada antes da lista estável: presente=%v erro=%v", exists, err)
	}
	waitFor(t, func() bool {
		cursor, err := c.SyncCursor(ctx, 1)
		if err != nil || cursor != 3 {
			return false
		}
		conv, ok, err := c.GetConversation(ctx, 1)
		if err != nil || !ok || conv.UnreadCount != 2 || !conv.Members[1].Online {
			return false
		}
		messages, err := c.ListMessages(ctx, 1, 0, 10)
		if err != nil || len(messages) != 3 {
			return false
		}
		remoteSaved, exists, err := c.GetConversation(ctx, 2)
		if err != nil || !exists || remoteSaved.UnreadCount != 1 {
			return false
		}
		remoteCursor, err := c.SyncCursor(ctx, 2)
		if err != nil || remoteCursor != 1 {
			return false
		}
		remoteMessages, err := c.ListMessages(ctx, 2, 0, 10)
		return err == nil && len(remoteMessages) == 1 && calls.Load() == 4
	})
}

func TestCompleteSyncKeepsCursorWhenRESTHistoryHasGap(t *testing.T) {
	ctx := context.Background()
	c, err := cache.Open(filepath.Join(t.TempDir(), "cliente.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	first := api.Message{ID: 1, ConversationID: 1, Sequence: 1, SenderID: 2, ClientID: "1"}
	if err := c.SaveConversations(ctx, []api.Conversation{{ID: 1, Type: api.ConversationDirect,
		LastSequence: 1, LastMessage: &first}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SyncCursor(ctx, 1); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/conversations" {
			json.NewEncoder(w).Encode([]api.Conversation{{ID: 1, Type: api.ConversationDirect, LastSequence: 3}})
			return
		}
		json.NewEncoder(w).Encode([]api.Message{first, {
			ID: 3, ConversationID: 1, Sequence: 3, SenderID: 2, ClientID: "3",
		}})
	}))
	defer server.Close()
	s := &session{ctx: ctx, cache: c, syncGeneration: 1}
	s.queue = queue.New(c, func(api.Message) error { return nil }, queue.Options{})
	a := &App{api: api.NewClient(server.URL), session: s, emit: func(string, ...any) {}}
	a.completeSync(s, 1, map[int64]int64{1: 1}, nil)
	cursor, err := c.SyncCursor(ctx, 1)
	if err != nil || cursor != 1 {
		t.Fatalf("cursor após lacuna = %d, erro = %v; queria manter 1", cursor, err)
	}
}
