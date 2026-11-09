package queue

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/api"
	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/cache"
)

// harness junta uma fila sobre um cache real e um "servidor" que registra os client_ids enviados.
type harness struct {
	t      *testing.T
	path   string
	cache  *cache.Cache
	queue  *Queue
	sent   []string
	online bool
	clock  time.Time
}

func newHarness(t *testing.T, path string) *harness {
	t.Helper()
	c, err := cache.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.SaveConversations(context.Background(), []api.Conversation{{ID: 1, Type: "direct"}}); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, path: path, cache: c, online: true, clock: time.Unix(1000, 0)}
	h.queue = New(c, func(msg api.Message) error {
		if !h.online {
			return errors.New("sem conexão")
		}
		h.sent = append(h.sent, msg.ClientID)
		return nil
	}, Options{AckTimeout: 10 * time.Second})
	h.queue.now = func() time.Time { return h.clock }
	return h
}

func (h *harness) add(clientID string) {
	h.t.Helper()
	msg := api.Message{ClientID: clientID, ConversationID: 1, SenderID: 7, Body: "msg " + clientID, CreatedAt: h.clock}
	if err := h.queue.Add(context.Background(), msg); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) pending() []string {
	h.t.Helper()
	items, err := h.cache.PendingSends(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	ids := []string{}
	for _, m := range items {
		ids = append(ids, m.ClientID)
	}
	return ids
}

func (h *harness) takeSent() []string {
	sent := h.sent
	h.sent = nil
	if sent == nil {
		return []string{}
	}
	return sent
}

func expect(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %v, esperava %v", what, got, want)
	}
}

func TestAddSendsImmediatelyAndAckRemoves(t *testing.T) {
	h := newHarness(t, filepath.Join(t.TempDir(), "q.db"))
	h.add("a")
	h.add("b")
	expect(t, "enviadas", h.takeSent(), []string{"a", "b"})
	expect(t, "fila", h.pending(), []string{"a", "b"})

	h.queue.Ack(context.Background(), "a")
	expect(t, "fila após ACK", h.pending(), []string{"b"})

	h.queue.Fail(context.Background(), "b")
	expect(t, "fila após erro", h.pending(), []string{})
}

func TestOfflineMessagesAreSentInOrderOnReconnect(t *testing.T) {
	h := newHarness(t, filepath.Join(t.TempDir(), "q.db"))
	h.add("a")
	h.online = false
	h.add("b")
	h.add("c")
	expect(t, "enviadas", h.takeSent(), []string{"a"})

	// Reconexão: a fila inteira vai de novo, na ordem original (inclusive "a", que pode ter se perdido).
	h.online = true
	h.queue.ResendAll(context.Background())
	expect(t, "reenviadas", h.takeSent(), []string{"a", "b", "c"})
}

func TestFailedSendStopsSoLaterMessagesDoNotJumpAhead(t *testing.T) {
	h := newHarness(t, filepath.Join(t.TempDir(), "q.db"))
	h.online = false
	h.add("a")
	h.online = true
	calls := 0
	h.queue.send = func(msg api.Message) error {
		calls++
		if msg.ClientID == "a" {
			return errors.New("buffer cheio")
		}
		h.sent = append(h.sent, msg.ClientID)
		return nil
	}
	h.add("b")
	expect(t, "enviadas", h.takeSent(), []string{})
	if calls != 1 {
		t.Fatalf("send chamado %d vezes; deveria parar em a", calls)
	}
}

func TestRetryDueResendsOnlyAfterTimeout(t *testing.T) {
	h := newHarness(t, filepath.Join(t.TempDir(), "q.db"))
	h.add("a")
	h.clock = h.clock.Add(5 * time.Second)
	h.add("b")
	h.takeSent()

	h.clock = h.clock.Add(6 * time.Second) // "a" com 11s sem ACK, "b" com 6s
	h.queue.RetryDue(context.Background())
	expect(t, "reenviadas após 11s", h.takeSent(), []string{"a"})

	h.queue.Ack(context.Background(), "a")
	h.clock = h.clock.Add(5 * time.Second)
	h.queue.RetryDue(context.Background())
	expect(t, "reenviadas após mais 5s", h.takeSent(), []string{"b"})
}

func TestQueueSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db")
	h := newHarness(t, path)
	h.online = false
	h.add("a")
	h.add("b")
	h.cache.Close()

	// "Reabrir o app": novo cache e nova fila sobre o mesmo arquivo.
	h2 := newHarness(t, path)
	h2.queue.ResendAll(context.Background())
	expect(t, "enviadas após reinício", h2.takeSent(), []string{"a", "b"})

	msgs, _ := h2.cache.ListMessages(context.Background(), 1, 0, 10)
	if len(msgs) != 2 || msgs[0].Status != api.StatusPending || msgs[0].SenderID != 7 {
		t.Fatalf("mensagens pendentes no cache = %+v", msgs)
	}
}
