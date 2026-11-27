package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/api"
	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/cache"
)

func TestDraftMethodsStayInTheLoggedInAccount(t *testing.T) {
	ctx := context.Background()
	c, err := cache.Open(filepath.Join(t.TempDir(), "ana.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.SaveConversations(ctx, []api.Conversation{{ID: 1, Type: api.ConversationDirect}}); err != nil {
		t.Fatal(err)
	}
	a := &App{session: &session{user: api.User{ID: 1}, cache: c, ctx: ctx}}

	if err := a.SaveDraft(1, 1, "mensagem da Ana"); err != nil {
		t.Fatal(err)
	}
	if got, err := a.LoadDraft(1, 1); err != nil || got != "mensagem da Ana" {
		t.Fatalf("rascunho da Ana = %q, erro = %v", got, err)
	}
	if err := a.SaveDraft(2, 1, "mensagem de outra conta"); err == nil {
		t.Fatal("outra conta alterou o rascunho")
	}
	if _, err := a.LoadDraft(2, 1); err == nil {
		t.Fatal("outra conta leu o rascunho")
	}
	if got, err := a.LoadDraft(1, 1); err != nil || got != "mensagem da Ana" {
		t.Fatalf("rascunho da Ana mudou = %q, erro = %v", got, err)
	}
}
