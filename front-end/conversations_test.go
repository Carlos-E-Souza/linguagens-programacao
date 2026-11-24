package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/api"
	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/cache"
)

func TestNewEmptyConversationRefreshesPresence(t *testing.T) {
	ctx := context.Background()
	c, err := cache.Open(filepath.Join(t.TempDir(), "cliente.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })

	conversation := api.Conversation{ID: 42, Type: api.ConversationDirect,
		Members: []api.Member{{UserID: 1}, {UserID: 2}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fresh := conversation
		fresh.Members = []api.Member{{UserID: 1}, {UserID: 2, Online: true}}
		json.NewEncoder(w).Encode([]api.Conversation{fresh})
	}))
	t.Cleanup(server.Close)

	s := &session{user: api.User{ID: 1}, cache: c, ctx: ctx}
	a := &App{api: api.NewClient(server.URL), session: s, emit: func(string, ...any) {}}
	a.handleConversationUpdated(s, raw(t, map[string]any{"conversation": conversation}))

	waitFor(t, func() bool {
		saved, ok, err := c.GetConversation(ctx, 42)
		if err != nil || !ok {
			return false
		}
		for _, member := range saved.Members {
			if member.UserID == 2 {
				return member.Online
			}
		}
		return false
	})
}

func TestExistingGroupRefreshesNewMemberPresence(t *testing.T) {
	ctx := context.Background()
	c, err := cache.Open(filepath.Join(t.TempDir(), "cliente.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })

	group := api.Conversation{ID: 42, Type: api.ConversationGroup, Name: "grupo",
		Members: []api.Member{{UserID: 1}, {UserID: 2}}}
	if err := c.SaveConversations(ctx, []api.Conversation{group}); err != nil {
		t.Fatal(err)
	}
	updated := group
	updated.Members = append([]api.Member{}, group.Members...)
	updated.Members = append(updated.Members, api.Member{UserID: 3})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fresh := updated
		fresh.Members = append([]api.Member{}, updated.Members...)
		fresh.Members[2].Online = true
		json.NewEncoder(w).Encode([]api.Conversation{fresh})
	}))
	t.Cleanup(server.Close)

	s := &session{user: api.User{ID: 1}, cache: c, ctx: ctx}
	a := &App{api: api.NewClient(server.URL), session: s, emit: func(string, ...any) {}}
	a.handleConversationUpdated(s, raw(t, map[string]any{"conversation": updated}))
	waitFor(t, func() bool {
		saved, ok, err := c.GetConversation(ctx, 42)
		return err == nil && ok && len(saved.Members) == 3 && saved.Members[2].Online
	})
}

func TestRefreshConversationsRetriesAfterLiveUpdate(t *testing.T) {
	ctx := context.Background()
	c, err := cache.Open(filepath.Join(t.TempDir(), "cliente.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	old := api.Conversation{ID: 1, Type: api.ConversationDirect,
		Members: []api.Member{{UserID: 1}, {UserID: 2}}}
	newConversation := api.Conversation{ID: 2, Type: api.ConversationGroup, Name: "novo",
		Members: []api.Member{{UserID: 1}, {UserID: 3}}}
	if err := c.SaveConversations(ctx, []api.Conversation{old}); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	message := api.Message{ID: 10, ConversationID: 1, Sequence: 1, SenderID: 2,
		ClientID: "nova", Body: "oi", CreatedAt: time.Now()}
	release := make(chan struct{})
	var releaseOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			<-release // a primeira resposta representa a lista antes do evento
			json.NewEncoder(w).Encode([]api.Conversation{old})
			return
		}
		fresh := old
		fresh.Members = []api.Member{{UserID: 1}, {UserID: 2, Online: true}}
		fresh.LastSequence = 1
		fresh.UnreadCount = 1
		fresh.LastMessage = &message
		json.NewEncoder(w).Encode([]api.Conversation{fresh, newConversation})
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	s := &session{user: api.User{ID: 1}, cache: c, ctx: ctx}
	a := &App{api: api.NewClient(server.URL), session: s, emit: func(string, ...any) {}}
	cached, err := c.ListConversations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.refreshConversations(s, cached)
	}()
	waitFor(t, func() bool { return calls.Load() == 1 })
	if _, err := c.ApplyConversationUpdate(ctx, newConversation); err != nil {
		t.Fatal(err)
	}
	if err := c.SetPresence(ctx, 2, true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SaveReceived(ctx, []api.Message{message}, 1); err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("atualização REST não terminou")
	}
	if calls.Load() != 2 {
		t.Fatalf("requisições REST = %d, esperado 2", calls.Load())
	}
	if _, ok, err := c.GetConversation(ctx, 2); err != nil || !ok {
		t.Fatalf("a conversa nova foi apagada: presente=%v erro=%v", ok, err)
	}
	saved, _, err := c.GetConversation(ctx, 1)
	if err != nil || !saved.Members[1].Online || saved.UnreadCount != 1 {
		t.Fatalf("presença ou não lidas regrediram: conversa=%+v erro=%v", saved, err)
	}
}
