package cache

import (
	"context"
	"testing"

	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/api"
)

func TestApplyConversationUpdateKeepsUnreadAndPresence(t *testing.T) {
	ctx := context.Background()
	c := openWithSession(t)
	if err := c.SaveConversations(ctx, []api.Conversation{
		{ID: 1, Type: api.ConversationDirect, UnreadCount: 4, Members: []api.Member{{UserID: 1}, {UserID: 2, Online: true}}},
	}); err != nil {
		t.Fatal(err)
	}

	// Grupo novo: o evento não traz presença, mas o usuário 2 já é conhecido como online.
	existed, err := c.ApplyConversationUpdate(ctx, api.Conversation{ID: 2, Type: api.ConversationGroup, Name: "LP",
		LastSequence: 7, Members: []api.Member{{UserID: 1, Role: "admin"}, {UserID: 2}, {UserID: 3}}})
	if err != nil || existed {
		t.Fatalf("existed = %v, erro = %v", existed, err)
	}
	group, ok, _ := c.GetConversation(ctx, 2)
	if !ok || group.Name != "LP" || len(group.Members) != 3 {
		t.Fatalf("grupo = %+v", group)
	}
	for _, m := range group.Members {
		if m.Online != (m.UserID == 2) {
			t.Fatalf("membro %d online = %v", m.UserID, m.Online)
		}
	}
	if cursor, _ := c.SyncCursor(ctx, 2); cursor != 7 {
		t.Fatalf("cursor do grupo novo = %d, quero 7", cursor)
	}

	// Conversa existente: membros mudam, não lidas ficam.
	existed, err = c.ApplyConversationUpdate(ctx, api.Conversation{ID: 1, Type: api.ConversationDirect,
		Members: []api.Member{{UserID: 1}, {UserID: 2}}})
	if err != nil || !existed {
		t.Fatalf("existed = %v, erro = %v", existed, err)
	}
	direct, _, _ := c.GetConversation(ctx, 1)
	if direct.UnreadCount != 4 {
		t.Fatalf("não lidas = %d, quero 4", direct.UnreadCount)
	}
}

func TestDeleteConversationDropsMessagesAndQueue(t *testing.T) {
	ctx := context.Background()
	c := openWithSession(t)
	if err := c.SaveConversations(ctx, []api.Conversation{{ID: 1, Type: api.ConversationGroup,
		Members: []api.Member{{UserID: 1}, {UserID: 2}}}}); err != nil {
		t.Fatal(err)
	}
	if err := c.EnqueueSend(ctx, api.Message{ConversationID: 1, SenderID: 1, ClientID: "a", Body: "oi"}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteConversation(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := c.GetConversation(ctx, 1); ok {
		t.Fatal("conversa continua no cache")
	}
	pending, _ := c.PendingSends(ctx)
	messages, _ := c.ListMessages(ctx, 1, 0, 10)
	if len(pending) != 0 || len(messages) != 0 {
		t.Fatalf("fila = %d, mensagens = %d", len(pending), len(messages))
	}
}
