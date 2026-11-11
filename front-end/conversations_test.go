package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

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
