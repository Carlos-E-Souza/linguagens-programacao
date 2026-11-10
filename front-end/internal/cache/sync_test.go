package cache

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/api"
)

func TestSyncCursorOnlyAdvancesAfterCompleteBackfill(t *testing.T) {
	ctx := context.Background()
	c := openTest(t)
	last := message(1, 5, 2, "five")
	if err := c.SaveConversations(ctx, []api.Conversation{{
		ID: 1, Type: api.ConversationDirect, LastSequence: 5, LastMessage: &last,
	}}); err != nil {
		t.Fatal(err)
	}
	check := func(want int64) {
		t.Helper()
		got, err := c.SyncCursor(ctx, 1)
		if err != nil || got != want {
			t.Fatalf("cursor = %d, erro = %v; queria %d", got, err, want)
		}
	}
	check(5)
	if err := c.SaveMessages(ctx, []api.Message{message(1, 6, 2, "six")}); err != nil {
		t.Fatal(err)
	}
	check(5) // Uma mensagem guardada ainda não prova que não há lacunas.
	if err := c.SaveSyncCursor(ctx, 1, 6); err != nil {
		t.Fatal(err)
	}
	check(6)
	if err := c.SaveSyncCursor(ctx, 1, 4); err != nil {
		t.Fatal(err)
	}
	check(6) // Um resultado antigo não pode fazer o cursor voltar.
}

func TestOpeningOldCacheFreezesCursorBeforeRESTRefresh(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "antigo.db")
	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	last := message(1, 5, 2, "five")
	if err := c.SaveConversations(ctx, []api.Conversation{{ID: 1, LastSequence: 5, LastMessage: &last}}); err != nil {
		t.Fatal(err)
	}
	c.Close()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE sync_cursors`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	c, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	newLast := message(1, 8, 2, "eight")
	if err := c.SaveConversations(ctx, []api.Conversation{{ID: 1, LastSequence: 8, LastMessage: &newLast}}); err != nil {
		t.Fatal(err)
	}
	cursor, err := c.SyncCursor(ctx, 1)
	if err != nil || cursor != 5 {
		t.Fatalf("cursor = %d, erro = %v; queria 5", cursor, err)
	}
}
