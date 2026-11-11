//go:build e2e

// Teste de ponta a ponta contra o back-end real: vários App (a mesma camada Go usada pela janela) conversam pelo
// servidor, sem a interface. Roda só com a tag e2e e com o servidor no ar:
//
//	cd back-end && DEV_MODE=true DATABASE_PATH=/tmp/e2e.db SERVER_ADDRESS=:18080 go run ./cmd/server
//	cd front-end && E2E_SERVER_URL=http://localhost:18080 go test -tags 'webkit2_41 e2e' -run E2E -v .
package main

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/api"
	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/events"
)

const e2eWait = 10 * time.Second

// e2eClient é um App com os eventos gravados para o teste esperar por eles.
type e2eClient struct {
	t    *testing.T
	app  *App
	user api.User

	mu     sync.Mutex
	events []emitted
	signal chan struct{}
}

func newE2EClient(t *testing.T) *e2eClient {
	c := &e2eClient{t: t, app: NewApp(), signal: make(chan struct{}, 1)}
	c.app.emit = func(name string, data ...any) {
		var payload any
		if len(data) > 0 {
			payload = data[0]
		}
		c.mu.Lock()
		c.events = append(c.events, emitted{name, payload})
		c.mu.Unlock()
		select {
		case c.signal <- struct{}{}:
		default:
		}
	}
	t.Cleanup(c.app.Logout)
	return c
}

// waitFor espera um evento com o nome dado que satisfaça match, olhando também os que já chegaram.
func (c *e2eClient) waitFor(name string, match func(any) bool) any {
	c.t.Helper()
	deadline := time.After(e2eWait)
	seen := 0
	for {
		c.mu.Lock()
		pending := c.events[seen:]
		seen = len(c.events)
		c.mu.Unlock()
		for _, ev := range pending {
			if ev.name == name && (match == nil || match(ev.data)) {
				return ev.data
			}
		}
		select {
		case <-c.signal:
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			c.t.Fatalf("%s: evento %s não chegou", c.user.Username, name)
		}
	}
}

// waitUntil espera cond ficar verdadeira.
func (c *e2eClient) waitUntil(what string, cond func() bool) {
	c.t.Helper()
	deadline := time.Now().Add(e2eWait)
	for !cond() {
		if time.Now().After(deadline) {
			c.t.Fatalf("%s: %s não aconteceu", c.user.Username, what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (c *e2eClient) login(username string) {
	c.t.Helper()
	user, err := c.app.Login(username, "senha1234")
	if err != nil {
		c.t.Fatalf("login %s: %v", username, err)
	}
	c.user = user
	c.waitUntil("conectar e sincronizar", func() bool {
		s := c.app.current()
		if s == nil || c.app.ConnectionState() != events.StateConnected {
			return false
		}
		s.syncMu.Lock()
		defer s.syncMu.Unlock()
		return !s.syncActive
	})
}

func (c *e2eClient) conversation(id int64) (api.Conversation, bool) {
	s := c.app.current()
	if s == nil {
		return api.Conversation{}, false
	}
	conv, ok, _ := s.cache.GetConversation(s.ctx, id)
	return conv, ok
}

func TestE2EConversationFlow(t *testing.T) {
	server := os.Getenv("E2E_SERVER_URL")
	if server == "" {
		t.Skip("defina E2E_SERVER_URL com o endereço do back-end")
	}
	t.Setenv("SERVER_URL", server)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	suffix := fmt.Sprint(time.Now().UnixNano() % 1_000_000)
	names := map[string]string{"alice": "alice_" + suffix, "bruno": "bruno_" + suffix, "carla": "carla_" + suffix}

	alice, bruno, carla := newE2EClient(t), newE2EClient(t), newE2EClient(t)
	for key, client := range map[string]*e2eClient{"alice": alice, "bruno": bruno, "carla": carla} {
		if _, err := client.app.Register(names[key], key, "senha1234"); err != nil {
			t.Fatalf("cadastro %s: %v", key, err)
		}
	}
	alice.login(names["alice"])
	bruno.login(names["bruno"])

	// Busca e conversa individual criada pela interface; o outro lado recebe conversation.updated.
	found, err := alice.app.SearchUsers(names["bruno"])
	if err != nil || len(found) != 1 || !found[0].Online {
		t.Fatalf("busca = %+v, erro = %v", found, err)
	}
	direct, err := alice.app.CreateDirectConversation(found[0].ID)
	if err != nil {
		t.Fatalf("criar conversa: %v", err)
	}
	bruno.waitFor(events.ConversationUpdated, func(d any) bool { return d.(api.Conversation).ID == direct.ID })

	// Envio, ACK, entrega e leitura.
	sent, err := alice.app.SendMessage(direct.ID, "oi, bruno")
	if err != nil {
		t.Fatalf("enviar: %v", err)
	}
	ack := alice.waitFor(events.MessageUpdated, func(d any) bool {
		return d.(events.MessageUpdatedPayload).ClientID == sent.ClientID
	}).(events.MessageUpdatedPayload)
	// O ACK espera a gravação no banco; a confirmação de entrega do bruno pode chegar antes e o status já vir
	// como delivered.
	if (ack.Status != api.StatusSent && ack.Status != api.StatusDelivered) || ack.Sequence == 0 {
		t.Fatalf("ACK = %+v", ack)
	}
	bruno.waitFor(events.MessageNew, func(d any) bool { return d.(api.Message).ClientID == sent.ClientID })
	alice.waitFor(events.MessageStatus, func(d any) bool {
		p := d.(events.MessageStatusPayload)
		return p.Status == api.StatusDelivered && p.UpToSequence >= ack.Sequence
	})
	if conv, _ := bruno.conversation(direct.ID); conv.UnreadCount != 1 {
		t.Fatalf("não lidas do bruno = %d", conv.UnreadCount)
	}
	if err := bruno.app.MarkAsRead(direct.ID, ack.Sequence); err != nil {
		t.Fatal(err)
	}
	alice.waitFor(events.MessageStatus, func(d any) bool {
		p := d.(events.MessageStatusPayload)
		return p.Status == api.StatusRead && p.UpToSequence >= ack.Sequence
	})

	// Presença e envio com o destinatário offline: chega na sincronização ao entrar de novo.
	bruno.app.Logout()
	alice.waitFor(events.Presence, func(d any) bool {
		p := d.(events.PresencePayload)
		return p.UserID == bruno.user.ID && !p.Online
	})
	offline, err := alice.app.SendMessage(direct.ID, "você estava fora")
	if err != nil {
		t.Fatal(err)
	}
	alice.waitFor(events.MessageUpdated, func(d any) bool {
		return d.(events.MessageUpdatedPayload).ClientID == offline.ClientID
	})
	bruno.login(names["bruno"])
	alice.waitFor(events.Presence, func(d any) bool {
		p := d.(events.PresencePayload)
		return p.UserID == bruno.user.ID && p.Online
	})
	history, err := bruno.app.LoadMessages(direct.ID, 0)
	if err != nil || len(history) != 2 || history[1].ClientID != offline.ClientID {
		t.Fatalf("histórico do bruno = %+v, erro = %v", history, err)
	}
	if conv, _ := bruno.conversation(direct.ID); conv.UnreadCount != 1 {
		t.Fatalf("não lidas do bruno depois de voltar = %d", conv.UnreadCount)
	}
	// Mensagens próprias não contam como não lidas, nem depois de atualizar pelo REST.
	if _, err := alice.app.ListConversations(); err != nil {
		t.Fatal(err)
	}
	alice.waitUntil("lista atualizada", func() bool {
		conv, ok := alice.conversation(direct.ID)
		return ok && conv.UnreadCount == 0
	})

	// Grupo: criação, mensagem, permissões, remoção e saída.
	carla.login(names["carla"])
	group, err := alice.app.CreateGroup("Projeto de LP", []int64{bruno.user.ID})
	if err != nil {
		t.Fatalf("criar grupo: %v", err)
	}
	bruno.waitFor(events.ConversationUpdated, func(d any) bool { return d.(api.Conversation).ID == group.ID })
	if err := alice.app.AddMembers(group.ID, []int64{carla.user.ID}); err != nil {
		t.Fatalf("adicionar: %v", err)
	}
	carla.waitFor(events.ConversationUpdated, func(d any) bool {
		return d.(api.Conversation).ID == group.ID && len(d.(api.Conversation).Members) == 3
	})

	groupMsg, err := carla.app.SendMessage(group.ID, "cheguei")
	if err != nil {
		t.Fatal(err)
	}
	alice.waitFor(events.MessageNew, func(d any) bool { return d.(api.Message).ClientID == groupMsg.ClientID })
	bruno.waitFor(events.MessageNew, func(d any) bool { return d.(api.Message).ClientID == groupMsg.ClientID })
	carla.waitFor(events.MessageStatus, func(d any) bool {
		p := d.(events.MessageStatusPayload)
		return p.ConversationID == group.ID && p.Status == api.StatusDelivered
	})

	if err := bruno.app.RemoveMember(group.ID, carla.user.ID); err == nil ||
		err.Error() != "Só administradores podem remover outros membros." {
		t.Fatalf("membro comum removendo outro: erro = %v", err)
	}
	if err := alice.app.RemoveMember(group.ID, carla.user.ID); err != nil {
		t.Fatalf("admin removendo: %v", err)
	}
	carla.waitFor(events.ConversationRemoved, func(d any) bool {
		return d.(events.ConversationRemovedPayload).ConversationID == group.ID
	})
	if _, ok := carla.conversation(group.ID); ok {
		t.Fatal("grupo continua no cache da carla")
	}

	if err := bruno.app.RemoveMember(group.ID, bruno.user.ID); err != nil {
		t.Fatalf("sair do grupo: %v", err)
	}
	if _, ok := bruno.conversation(group.ID); ok {
		t.Fatal("grupo continua no cache do bruno depois de sair")
	}
	alice.waitFor(events.ConversationUpdated, func(d any) bool {
		c := d.(api.Conversation)
		return c.ID == group.ID && len(c.Members) == 1
	})
}
