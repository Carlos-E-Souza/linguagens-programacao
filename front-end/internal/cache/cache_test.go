package cache

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/api"
)

func openTest(t *testing.T) *Cache {
	t.Helper()
	c, err := Open(filepath.Join(t.TempDir(), "teste.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

var base = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

func message(conv, seq, sender int64, clientID string) api.Message {
	return api.Message{
		ID: seq * 10, ConversationID: conv, Sequence: seq, SenderID: sender, ClientID: clientID,
		Body: "msg " + clientID, CreatedAt: base.Add(time.Duration(seq) * time.Minute),
	}
}

func TestSessionRoundTripAndClear(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ana.db")
	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	user := api.User{ID: 1, Username: "ana", DisplayName: "Ana"}
	if err := c.SaveSession(ctx, "tok", user); err != nil {
		t.Fatal(err)
	}
	c.Close()

	// Reabrir o banco mantém a sessão.
	c, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	token, got, ok, err := c.LoadSession(ctx)
	if err != nil || !ok || token != "tok" || got != user {
		t.Fatalf("sessão = %q %+v %v %v", token, got, ok, err)
	}

	if err := c.ClearSession(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, _ := c.LoadSession(ctx); ok {
		t.Fatal("sessão deveria ter sido apagada")
	}
}

func TestConversationsOrderedByActivityWithMembersAndLastMessage(t *testing.T) {
	ctx := context.Background()
	c := openTest(t)
	last := message(1, 3, 2, "c")
	err := c.SaveConversations(ctx, []api.Conversation{
		{ID: 1, Type: api.ConversationDirect, CreatedAt: base, LastSequence: 3, LastMessage: &last,
			Members: []api.Member{{UserID: 1, DisplayName: "Ana"}, {UserID: 2, DisplayName: "Bruno", Online: true}}},
		{ID: 2, Type: api.ConversationGroup, Name: "Equipe", CreatedAt: base.Add(time.Hour)},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := c.ListConversations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != 2 || got[1].ID != 1 {
		t.Fatalf("ordem inesperada: %+v", got)
	}
	if len(got[1].Members) != 2 || !got[1].Members[1].Online {
		t.Errorf("membros = %+v", got[1].Members)
	}
	if got[1].LastMessage == nil || got[1].LastMessage.ClientID != "c" {
		t.Errorf("última mensagem = %+v", got[1].LastMessage)
	}

	// Uma mensagem nova em 1 a coloca no topo.
	if err := c.SaveMessages(ctx, []api.Message{message(1, 90, 2, "d")}); err != nil {
		t.Fatal(err)
	}
	got, _ = c.ListConversations(ctx)
	if got[0].ID != 1 || got[0].LastSequence != 90 {
		t.Fatalf("conversa 1 deveria estar no topo com sequência 90: %+v", got[0])
	}
}

func TestReplaceConversationsRemovesMissing(t *testing.T) {
	ctx := context.Background()
	c := openTest(t)
	c.SaveConversations(ctx, []api.Conversation{{ID: 1, Type: "direct"}, {ID: 2, Type: "group"}})
	c.SaveMessages(ctx, []api.Message{message(2, 1, 1, "a")})

	removed, err := c.ReplaceConversations(ctx, []api.Conversation{{ID: 1, Type: "direct"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != 2 {
		t.Fatalf("removidas = %v", removed)
	}
	if msgs, _ := c.ListMessages(ctx, 2, 0, 10); len(msgs) != 0 {
		t.Errorf("mensagens da conversa removida deveriam sumir: %+v", msgs)
	}
}

func TestMessagesPaginationAndPendingAtEnd(t *testing.T) {
	ctx := context.Background()
	c := openTest(t)
	c.SaveConversations(ctx, []api.Conversation{{ID: 1, Type: "direct"}})
	var msgs []api.Message
	for seq := int64(1); seq <= 5; seq++ {
		msgs = append(msgs, message(1, seq, 2, string(rune('a'+seq))))
	}
	pending := api.Message{ConversationID: 1, SenderID: 1, ClientID: "p", Body: "pendente",
		CreatedAt: base, Status: api.StatusPending}
	msgs = append(msgs, pending)
	if err := c.SaveMessages(ctx, msgs); err != nil {
		t.Fatal(err)
	}

	latest, _ := c.ListMessages(ctx, 1, 0, 3)
	if len(latest) != 3 || latest[0].Sequence != 4 || latest[1].Sequence != 5 || latest[2].ClientID != "p" {
		t.Fatalf("mais recentes = %+v", latest)
	}
	older, _ := c.ListMessages(ctx, 1, 4, 10)
	if len(older) != 3 || older[0].Sequence != 1 || older[2].Sequence != 3 {
		t.Fatalf("página anterior = %+v", older)
	}
	if seq, _ := c.LastSequence(ctx, 1); seq != 5 {
		t.Errorf("LastSequence = %d", seq)
	}
}

func TestUpsertByClientIDCompletesPendingWithoutDuplicates(t *testing.T) {
	ctx := context.Background()
	c := openTest(t)
	c.SaveConversations(ctx, []api.Conversation{{ID: 1, Type: "direct"}})
	pending := api.Message{ConversationID: 1, SenderID: 1, ClientID: "x", Body: "oi", CreatedAt: base,
		Status: api.StatusPending}
	c.SaveMessages(ctx, []api.Message{pending})

	// O ACK/histórico traz id e sequência para o mesmo client_id.
	acked := pending
	acked.ID, acked.Sequence, acked.Status = 99, 7, api.StatusSent
	c.SaveMessages(ctx, []api.Message{acked})
	c.UpdateStatus(ctx, "x", api.StatusRead)

	// Rever a mensagem no histórico (status sent) não pode rebaixar "read".
	c.SaveMessages(ctx, []api.Message{acked})

	got, _ := c.ListMessages(ctx, 1, 0, 10)
	if len(got) != 1 {
		t.Fatalf("esperava 1 mensagem, veio %d: %+v", len(got), got)
	}
	if got[0].ID != 99 || got[0].Sequence != 7 || got[0].Status != api.StatusRead {
		t.Errorf("mensagem = %+v", got[0])
	}
}

func TestLastUserFile(t *testing.T) {
	dir := t.TempDir()
	if u, err := LoadLastUser(dir); err != nil || u != "" {
		t.Fatalf("sem arquivo: %q %v", u, err)
	}
	SaveLastUser(dir, "ana")
	if u, _ := LoadLastUser(dir); u != "ana" {
		t.Fatalf("último usuário = %q", u)
	}
	ClearLastUser(dir)
	if u, _ := LoadLastUser(dir); u != "" {
		t.Fatalf("deveria estar vazio: %q", u)
	}
	if got := PathFor(dir, "../Ana"); got != filepath.Join(dir, "___ana.db") {
		t.Errorf("PathFor não sanitizou: %s", got)
	}
}

func TestAckMessageAndRetryAfterFailure(t *testing.T) {
	ctx := context.Background()
	c := openTest(t)
	c.SaveConversations(ctx, []api.Conversation{{ID: 1, Type: "direct", CreatedAt: base}})
	msg := api.Message{ClientID: "x", ConversationID: 1, SenderID: 1, Body: "oi", CreatedAt: base}
	if err := c.EnqueueSend(ctx, msg); err != nil {
		t.Fatal(err)
	}

	acked, ok, err := c.AckMessage(ctx, "x", 99, 4, base.Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("AckMessage = %v %v", ok, err)
	}
	if acked.ID != 99 || acked.Sequence != 4 || acked.Status != api.StatusSent || !acked.CreatedAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("mensagem após ACK = %+v", acked)
	}
	if conv, _, _ := c.GetConversation(ctx, 1); conv.LastSequence != 4 {
		t.Errorf("last_sequence = %d", conv.LastSequence)
	}
	if _, ok, _ := c.AckMessage(ctx, "inexistente", 1, 1, base); ok {
		t.Error("ACK de mensagem desconhecida não deveria afetar nada")
	}

	// Falha e nova tentativa: volta para pending e para a fila.
	c.UpdateStatus(ctx, "y", api.StatusFailed)
	failed := api.Message{ClientID: "y", ConversationID: 1, SenderID: 1, Body: "de novo", CreatedAt: base}
	c.EnqueueSend(ctx, failed)
	c.RemoveSend(ctx, "y")
	c.UpdateStatus(ctx, "y", api.StatusFailed)
	if err := c.EnqueueSend(ctx, failed); err != nil {
		t.Fatal(err)
	}
	got, _, _ := c.GetMessage(ctx, "y")
	pending, _ := c.PendingSends(ctx)
	if got.Status != api.StatusPending || len(pending) != 2 || pending[1].ClientID != "y" {
		t.Fatalf("após nova tentativa: status %s, fila %+v", got.Status, pending)
	}
}
