package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/db"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
)

// newStoreFixture opens a migrated database and returns a real Store, so
// these tests exercise the actual SQL rather than a fake.
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

// seedConversation creates the users and conversation a message needs, so
// the foreign keys are satisfied.
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

// TestPoolPersistsEnqueuedMessages is the end-to-end guarantee: what the
// hub enqueues ends up in the database after a flush.
func TestPoolPersistsEnqueuedMessages(t *testing.T) {
	ctx := context.Background()
	store := newStoreFixture(t)
	conv := seedConversation(t, store)

	pool := NewPool(ctx, store, Options{
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

	// Wait for the flush rather than racing it.
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

	// The returned rows carry the assigned IDs and sequences.
	for i, msg := range messages {
		if msg.ID == 0 {
			t.Errorf("message %d has no ID", i)
		}
		if got, want := msg.Sequence, int64(i+1); got != want {
			t.Errorf("message %d sequence: got %d, want %d", i, got, want)
		}
	}

	// last_sequence must have advanced so the hub can prime itself.
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

// TestPoolShutdownDrainsQueue covers the graceful-shutdown promise: a
// message accepted before shutdown must still be written.
func TestPoolShutdownDrainsQueue(t *testing.T) {
	ctx := context.Background()
	store := newStoreFixture(t)
	conv := seedConversation(t, store)

	pool := NewPool(ctx, store, Options{
		Workers:     1,
		QueueSize:   64,
		BatchSize:   64,        // large, so nothing flushes early
		FlushEvery:  time.Hour, // effectively never
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

// TestPoolRejectsEnqueueAfterShutdown documents that the pool fails
// loudly rather than accepting a message it will never write.
func TestPoolRejectsEnqueueAfterShutdown(t *testing.T) {
	ctx := context.Background()
	store := newStoreFixture(t)
	conv := seedConversation(t, store)

	pool := NewPool(ctx, store, DefaultOptions(8, 1))

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

// TestPoolShutdownIsIdempotent guards against a double close panic, since
// several shutdown paths can run.
func TestPoolShutdownIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := newStoreFixture(t)

	pool := NewPool(ctx, store, DefaultOptions(8, 1))

	for i := 0; i < 3; i++ {
		if err := pool.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown() call %d failed: %v", i+1, err)
		}
	}
}
