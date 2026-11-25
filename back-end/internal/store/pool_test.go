package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/db"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
)

// newStoreFixture abre um banco migrado e devolve um Store real, para que
// estes testes exercitem o SQL de verdade em vez de um dublê.
func newStoreFixture(t *testing.T) *db.Store {
	t.Helper()

	handle, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatalf("db.Open() failed: %v", err)
	}
	t.Cleanup(func() { handle.Close() })

	if err := db.Migrate(context.Background(), handle); err != nil {
		t.Fatalf("db.Migrate() failed: %v", err)
	}

	return db.NewStore(handle)
}

// seedConversation cria os usuários e a conversa de que uma mensagem
// precisa, para satisfazer as chaves estrangeiras.
func seedConversation(t *testing.T, s *db.Store) models.Conversation {
	t.Helper()

	ctx := context.Background()

	alice, err := s.CreateUser(ctx, "alice", "Alice", "hash")
	if err != nil {
		t.Fatalf("CreateUser(alice) failed: %v", err)
	}
	bob, err := s.CreateUser(ctx, "bob", "Bob", "hash")
	if err != nil {
		t.Fatalf("CreateUser(bob) failed: %v", err)
	}

	conv, err := s.CreateConversation(ctx,
		models.ConversationDirect, "", alice.ID, []int64{alice.ID, bob.ID}, nil)
	if err != nil {
		t.Fatalf("CreateConversation() failed: %v", err)
	}

	return conv
}

// TestPoolPersistsEnqueuedMessages é a garantia de ponta a ponta: o que o
// hub enfileira acaba no banco depois de um despacho.
func TestPoolPersistsEnqueuedMessages(t *testing.T) {
	ctx := context.Background()
	store := newStoreFixture(t)
	conv := seedConversation(t, store)

	pool := NewPool(store, Options{
		Workers:     1,
		QueueSize:   16,
		BatchSize:   4,
		FlushEvery:  20 * time.Millisecond,
		EnqueueWait: time.Second,
	})

	for i := 1; i <= 3; i++ {
		msg := models.Message{
			ConversationID: conv.ID,
			Sequence:       int64(i),
			SenderID:       conv.CreatedBy,
			ClientID:       "client-" + string(rune('0'+i)),
			Body:           "body",
			CreatedAt:      time.Now().UTC(),
		}
		if _, err := pool.Enqueue(ctx, msg); err != nil {
			t.Fatalf("Enqueue(%d) failed: %v", i, err)
		}
	}

	// Espera o despacho em vez de disputar com ele.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		messages, err := store.ListMessages(ctx, conv.ID, 0, 10)
		if err != nil {
			t.Fatalf("ListMessages() failed: %v", err)
		}
		if len(messages) == 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	messages, err := store.ListMessages(ctx, conv.ID, 0, 10)
	if err != nil {
		t.Fatalf("ListMessages() failed: %v", err)
	}
	if got, want := len(messages), 3; got != want {
		t.Fatalf("persisted messages: got %d, want %d", got, want)
	}

	// As linhas devolvidas trazem os IDs e sequences atribuídos.
	for i, msg := range messages {
		if msg.ID == 0 {
			t.Errorf("message %d has no ID", i)
		}
		if got, want := msg.Sequence, int64(i+1); got != want {
			t.Errorf("message %d sequence: got %d, want %d", i, got, want)
		}
	}

	// last_sequence precisa ter avançado para o hub poder se inicializar.
	last, err := store.LastSequence(ctx, conv.ID)
	if err != nil {
		t.Fatalf("LastSequence() failed: %v", err)
	}
	if got, want := last, int64(3); got != want {
		t.Errorf("last_sequence: got %d, want %d", got, want)
	}

	if err := pool.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() failed: %v", err)
	}
}

// TestPoolShutdownDrainsQueue cobre a promessa do desligamento gracioso:
// uma mensagem aceita antes do desligamento ainda precisa ser gravada.
func TestPoolShutdownDrainsQueue(t *testing.T) {
	ctx := context.Background()
	store := newStoreFixture(t)
	conv := seedConversation(t, store)

	pool := NewPool(store, Options{
		Workers:     1,
		QueueSize:   64,
		BatchSize:   64,        // grande, para nada despachar antes da hora
		FlushEvery:  time.Hour, // na prática, nunca
		EnqueueWait: time.Second,
	})

	for i := 1; i <= 5; i++ {
		if _, err := pool.Enqueue(ctx, models.Message{
			ConversationID: conv.ID,
			Sequence:       int64(i),
			SenderID:       conv.CreatedBy,
			ClientID:       "drain-" + string(rune('0'+i)),
			Body:           "pending",
			CreatedAt:      time.Now().UTC(),
		}); err != nil {
			t.Fatalf("Enqueue(%d) failed: %v", i, err)
		}
	}

	if err := pool.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() failed: %v", err)
	}

	messages, err := store.ListMessages(ctx, conv.ID, 0, 10)
	if err != nil {
		t.Fatalf("ListMessages() failed: %v", err)
	}
	if got, want := len(messages), 5; got != want {
		t.Errorf("messages after shutdown: got %d, want %d (queue was not drained)", got, want)
	}
}

// TestPoolRejectsEnqueueAfterShutdown documenta que o pool falha de forma
// visível em vez de aceitar uma mensagem que nunca vai gravar.
func TestPoolRejectsEnqueueAfterShutdown(t *testing.T) {
	ctx := context.Background()
	store := newStoreFixture(t)
	conv := seedConversation(t, store)

	pool := NewPool(store, DefaultOptions(8, 1))

	if err := pool.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() failed: %v", err)
	}

	_, err := pool.Enqueue(ctx, models.Message{
		ConversationID: conv.ID,
		Sequence:       1,
		SenderID:       conv.CreatedBy,
		ClientID:       "late",
		Body:           "too late",
		CreatedAt:      time.Now().UTC(),
	})
	if err == nil {
		t.Error("expected an error when enqueuing into a closed pool")
	}
}

// TestPoolShutdownIsIdempotent protege contra um panic de fechamento
// duplo, já que vários caminhos de desligamento podem rodar.
func TestPoolShutdownIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := newStoreFixture(t)

	pool := NewPool(store, DefaultOptions(8, 1))

	for i := 0; i < 3; i++ {
		if err := pool.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown() call %d failed: %v", i+1, err)
		}
	}
}

// blockingWriter segura toda gravação até release ser fechado, para que um
// teste possa encher a fila atrás de um worker ocupado.
type blockingWriter struct {
	release chan struct{}
	started chan struct{}
	once    sync.Once

	mu      sync.Mutex
	written []models.Message
}

func (w *blockingWriter) InsertMessages(ctx context.Context, messages []models.Message) ([]models.Message, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release

	w.mu.Lock()
	defer w.mu.Unlock()
	w.written = append(w.written, messages...)

	stored := make([]models.Message, len(messages))
	for i, m := range messages {
		m.ID = int64(len(w.written) - len(messages) + i + 1)
		stored[i] = m
	}
	return stored, nil
}

// TestPoolFullQueueAppliesBackpressure cobre a Fase 6: com a fila cheia,
// Enqueue espera até EnqueueWait e então falha em vez de descartar a
// mensagem em silêncio, e tudo o que ele aceitou ainda é gravado quando o
// gravador se recupera.
func TestPoolFullQueueAppliesBackpressure(t *testing.T) {
	writer := &blockingWriter{release: make(chan struct{}), started: make(chan struct{})}

	pool := NewPool(writer, Options{
		Workers:     1,
		QueueSize:   1,
		BatchSize:   1,
		FlushEvery:  time.Hour,
		EnqueueWait: 50 * time.Millisecond,
	})

	msg := func(n int) models.Message {
		return models.Message{ConversationID: 1, Sequence: int64(n), SenderID: 1,
			ClientID: fmt.Sprintf("full-%d", n), Body: "x", CreatedAt: time.Now().UTC()}
	}
	ctx := context.Background()

	// A primeira mensagem é pega pelo worker, que então bloqueia.
	if _, err := pool.Enqueue(ctx, msg(1)); err != nil {
		t.Fatalf("Enqueue(1) failed: %v", err)
	}
	<-writer.started

	// A segunda ocupa a única vaga da fila.
	if _, err := pool.Enqueue(ctx, msg(2)); err != nil {
		t.Fatalf("Enqueue(2) failed: %v", err)
	}

	// A terceira não tem para onde ir: precisa esperar e então falhar.
	start := time.Now()
	if _, err := pool.Enqueue(ctx, msg(3)); err == nil {
		t.Fatal("Enqueue(3) succeeded on a full queue; want an error")
	}
	if waited := time.Since(start); waited < 40*time.Millisecond {
		t.Errorf("Enqueue(3) gave up after %s; want it to wait about EnqueueWait (50ms)", waited)
	}

	close(writer.release)

	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() failed: %v", err)
	}

	writer.mu.Lock()
	defer writer.mu.Unlock()
	if got, want := len(writer.written), 2; got != want {
		t.Errorf("messages written: got %d, want %d (both accepted messages)", got, want)
	}
}
