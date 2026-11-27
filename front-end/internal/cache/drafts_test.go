package cache

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/api"
)

func TestDraftPersistsAfterReopeningCache(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ana.db")
	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SaveConversations(ctx, []api.Conversation{{ID: 1, Type: api.ConversationDirect}}); err != nil {
		t.Fatal(err)
	}
	if err := c.SaveDraft(ctx, 1, "Olá, Bruno!\nAté amanhã."); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	c, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	got, err := c.LoadDraft(ctx, 1)
	if err != nil || got != "Olá, Bruno!\nAté amanhã." {
		t.Fatalf("rascunho após reabrir = %q, erro = %v", got, err)
	}
}
