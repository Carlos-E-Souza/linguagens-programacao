package cache

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/api"
)

func openWithSession(t *testing.T) *Cache {
	t.Helper()
	c, err := Open(filepath.Join(t.TempDir(), "cliente.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.SaveSession(context.Background(), "token", api.User{ID: 1, Username: "ana"}); err != nil {
		t.Fatal(err)
	}
	return c
}

func statuses(t *testing.T, c *Cache, conversationID int64) map[int64]string {
	t.Helper()
	messages, err := c.ListMessages(context.Background(), conversationID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[int64]string)
	for _, m := range messages {
		got[m.Sequence] = m.Status
	}
	return got
}

func TestApplyReceiptInGroupWaitsForAllMembers(t *testing.T) {
	ctx := context.Background()
	c := openWithSession(t)
	group := api.Conversation{ID: 5, Type: api.ConversationGroup, Members: []api.Member{
		{UserID: 1, Username: "ana"}, {UserID: 2, Username: "bruno"}, {UserID: 3, Username: "carla"},
	}}
	if err := c.SaveConversations(ctx, []api.Conversation{group}); err != nil {
		t.Fatal(err)
	}
	if err := c.SaveMessages(ctx, []api.Message{
		{ID: 1, ConversationID: 5, Sequence: 1, SenderID: 1, ClientID: "a"},
		{ID: 2, ConversationID: 5, Sequence: 2, SenderID: 1, ClientID: "b"},
		{ID: 3, ConversationID: 5, Sequence: 3, SenderID: 2, ClientID: "c"},
	}); err != nil {
		t.Fatal(err)
	}

	r, err := c.ApplyReceipt(ctx, 5, 2, 2, api.StatusRead)
	if err != nil || r.Delivered != 0 || r.Read != 0 {
		t.Fatalf("receipts = %+v, erro = %v", r, err)
	}
	if got := statuses(t, c, 5); got[1] != api.StatusSent || got[2] != api.StatusSent {
		t.Fatalf("status antes de todos confirmarem = %v", got)
	}

	r, err = c.ApplyReceipt(ctx, 5, 3, 1, api.StatusDelivered)
	if err != nil || r.Delivered != 1 || r.Read != 0 {
		t.Fatalf("receipts = %+v, erro = %v", r, err)
	}
	r, err = c.ApplyReceipt(ctx, 5, 3, 2, api.StatusRead)
	if err != nil || r.Delivered != 2 || r.Read != 2 {
		t.Fatalf("receipts = %+v, erro = %v", r, err)
	}
	got := statuses(t, c, 5)
	if got[1] != api.StatusRead || got[2] != api.StatusRead || got[3] != api.StatusSent {
		t.Fatalf("status = %v", got)
	}

	// Uma confirmação atrasada não faz a posição voltar.
	if r, err = c.ApplyReceipt(ctx, 5, 3, 1, api.StatusDelivered); err != nil || r.Read != 2 {
		t.Fatalf("receipts = %+v, erro = %v", r, err)
	}
}

func TestHistoryGetsStatusFromMemberPositions(t *testing.T) {
	ctx := context.Background()
	c := openWithSession(t)
	conv := api.Conversation{ID: 7, Type: api.ConversationDirect, Members: []api.Member{
		{UserID: 1}, {UserID: 2, LastDelivered: 2, LastRead: 1},
	}}
	if err := c.SaveConversations(ctx, []api.Conversation{conv}); err != nil {
		t.Fatal(err)
	}
	if err := c.SaveMessages(ctx, []api.Message{
		{ID: 1, ConversationID: 7, Sequence: 1, SenderID: 1, ClientID: "a"},
		{ID: 2, ConversationID: 7, Sequence: 2, SenderID: 1, ClientID: "b"},
		{ID: 3, ConversationID: 7, Sequence: 3, SenderID: 1, ClientID: "c"},
	}); err != nil {
		t.Fatal(err)
	}
	got := statuses(t, c, 7)
	if got[1] != api.StatusRead || got[2] != api.StatusDelivered || got[3] != api.StatusSent {
		t.Fatalf("status = %v", got)
	}
}

func TestMarkReadRecountsUnread(t *testing.T) {
	ctx := context.Background()
	c := openWithSession(t)
	conv := api.Conversation{ID: 9, Type: api.ConversationDirect, UnreadCount: 3,
		Members: []api.Member{{UserID: 1}, {UserID: 2}}}
	if err := c.SaveConversations(ctx, []api.Conversation{conv}); err != nil {
		t.Fatal(err)
	}
	if err := c.SaveMessages(ctx, []api.Message{
		{ID: 1, ConversationID: 9, Sequence: 1, SenderID: 2, ClientID: "a"},
		{ID: 2, ConversationID: 9, Sequence: 2, SenderID: 1, ClientID: "b"},
		{ID: 3, ConversationID: 9, Sequence: 3, SenderID: 2, ClientID: "c"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.MarkRead(ctx, 9, 1, 2); err != nil {
		t.Fatal(err)
	}
	saved, _, _ := c.GetConversation(ctx, 9)
	if saved.UnreadCount != 1 {
		t.Fatalf("não lidas = %d, quero 1", saved.UnreadCount)
	}
	// A 4 é nova e de outro membro; a 3 repetida não conta de novo.
	fresh, err := c.SaveReceived(ctx, []api.Message{
		{ID: 3, ConversationID: 9, Sequence: 3, SenderID: 2, ClientID: "c"},
		{ID: 4, ConversationID: 9, Sequence: 4, SenderID: 2, ClientID: "d"},
	}, 1)
	if err != nil || fresh != 1 {
		t.Fatalf("novas = %d, erro = %v", fresh, err)
	}
	saved, _, _ = c.GetConversation(ctx, 9)
	if saved.UnreadCount != 2 {
		t.Fatalf("não lidas = %d, quero 2", saved.UnreadCount)
	}
	if err := c.MarkRead(ctx, 9, 1, 4); err != nil {
		t.Fatal(err)
	}
	saved, _, _ = c.GetConversation(ctx, 9)
	if saved.UnreadCount != 0 {
		t.Fatalf("não lidas = %d, quero 0", saved.UnreadCount)
	}
}

func TestSetPresenceUpdatesEveryConversation(t *testing.T) {
	ctx := context.Background()
	c := openWithSession(t)
	if err := c.SaveConversations(ctx, []api.Conversation{
		{ID: 1, Type: api.ConversationDirect, Members: []api.Member{{UserID: 1}, {UserID: 2}}},
		{ID: 2, Type: api.ConversationGroup, Members: []api.Member{{UserID: 1}, {UserID: 2}, {UserID: 3}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetPresence(ctx, 2, true); err != nil {
		t.Fatal(err)
	}
	conversations, _ := c.ListConversations(ctx)
	for _, conv := range conversations {
		for _, m := range conv.Members {
			if m.Online != (m.UserID == 2) {
				t.Fatalf("conversa %d membro %d online = %v", conv.ID, m.UserID, m.Online)
			}
		}
	}
}
