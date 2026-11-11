package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/ws"
)

// heldPersister accepts messages but answers only when released, which
// keeps them in the persistence queue for as long as a test needs.
type heldPersister struct {
	mu      sync.Mutex
	waiting []chan models.Message
	held    []models.Message
}

func (p *heldPersister) Enqueue(ctx context.Context, msg models.Message) (<-chan models.Message, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ch := make(chan models.Message, 1)
	p.waiting = append(p.waiting, ch)
	p.held = append(p.held, msg)
	return ch, nil
}

// release answers every held message as "not stored".
func (p *heldPersister) release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ch := range p.waiting {
		ch <- models.Message{}
	}
	p.waiting = nil
}

// TestSyncIncludesMessagesStillInTheQueue covers Phase 8: a message that
// was accepted but not written yet is part of the backfill.
func TestSyncIncludesMessagesStillInTheQueue(t *testing.T) {
	f := newHubFixture(t)
	alice, bob, conv := f.createUsers(t)

	persister := &heldPersister{}
	h := New(f.hub.cfg, f.store, persister)
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); h.Run(ctx) }()
	defer func() { persister.release(); stop(); <-done }()

	f2 := &hubFixture{t: t, hub: h, store: f.store, ctx: ctx}

	aliceClient := f2.addClient(alice.ID)
	for i := 1; i <= 2; i++ {
		f2.submit(aliceClient, mustEnvelope(ws.TypeMessageSend, ws.SendPayload{
			ClientID: fmt.Sprintf("queued-%d", i), ConversationID: conv.ID, Body: "not written yet",
		}))
		// message.new is the sign the hub accepted it; the ACK waits on
		// the held persister.
		waitForType(t, aliceClient, ws.TypeMessageNew, 2*time.Second)
	}

	bobClient := f2.addClient(bob.ID)
	f2.submit(bobClient, mustEnvelope(ws.TypeSync, ws.SyncPayload{}))
	waitForType(t, bobClient, ws.TypeSyncEnd, 3*time.Second)

	var got []int64
	for {
		frame, ok := bobClient.claim(ws.TypeMessageNew)
		if !ok {
			break
		}
		var msg models.Message
		if err := json.Unmarshal(mustData(t, frame), &msg); err != nil {
			t.Fatalf("decode message.new: %v", err)
		}
		got = append(got, msg.Sequence)
	}
	if fmt.Sprint(got) != fmt.Sprint([]int64{1, 2}) {
		t.Errorf("backfill with an unwritten queue: got %v, want [1 2]", got)
	}
}

// TestMergeBackfill covers the merge of stored and unconfirmed messages.
func TestMergeBackfill(t *testing.T) {
	m := func(seq, id int64) models.Message { return models.Message{Sequence: seq, ID: id} }

	stored := []models.Message{m(3, 30), m(4, 40)}
	unsaved := []models.Message{m(2, 0), m(4, 0), m(6, 0), m(5, 0)}

	got := mergeBackfill(stored, unsaved, 2, 3)

	want := []models.Message{m(3, 30), m(4, 40), m(5, 0)}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("mergeBackfill: got %v, want %v (after 2, stored rows win, limit 3)", got, want)
	}
}

// TestSlowClientIsClosedWithPolicyViolation checks the close code the
// connection gets when the hub drops it for a full queue.
func TestSlowClientIsClosedWithPolicyViolation(t *testing.T) {
	f := newHubFixture(t)
	alice, _, _ := f.createUsers(t)

	client, _ := newClient(alice.ID, nil, 1)
	f.hub.Register(client)
	for deadline := time.Now().Add(time.Second); !f.hub.IsOnline(alice.ID); {
		if time.Now().After(deadline) {
			t.Fatal("client never became online")
		}
		time.Sleep(time.Millisecond)
	}

	client.trySend([]byte(`{"type":"filler"}`)) // queue is now full
	f.hub.deliverToUser(alice.ID, []byte(`{"type":"overflow"}`))

	if code, _ := client.CloseStatus(); code != websocket.StatusPolicyViolation {
		t.Errorf("close status: got %v, want %v", code, websocket.StatusPolicyViolation)
	}

	// The later regular unregister must not overwrite the code.
	f.hub.Unregister(client)
	if code, _ := client.CloseStatus(); code != websocket.StatusPolicyViolation {
		t.Errorf("close status after unregister: got %v, want %v", code, websocket.StatusPolicyViolation)
	}
}

// TestShutdownClosesWithGoingAway checks the close code of a graceful
// shutdown.
func TestShutdownClosesWithGoingAway(t *testing.T) {
	f := newHubFixture(t)
	alice, _, _ := f.createUsers(t)

	client := f.addClient(alice.ID)
	f.stop()

	if code, _ := client.CloseStatus(); code != websocket.StatusGoingAway {
		t.Errorf("close status: got %v, want %v", code, websocket.StatusGoingAway)
	}
}

// TestRemovedMemberReceivesConversationUpdate checks a user removed from a
// group gets the update listing the members without them.
func TestRemovedMemberReceivesConversationUpdate(t *testing.T) {
	f := newHubFixture(t)
	alice, bob, _ := f.createUsers(t)
	ctx := context.Background()

	group, err := f.store.CreateConversation(ctx, models.ConversationGroup, "team", alice.ID,
		[]int64{alice.ID, bob.ID}, map[int64]string{alice.ID: models.RoleAdmin})
	if err != nil {
		t.Fatalf("CreateConversation() failed: %v", err)
	}

	bobClient := f.addClient(bob.ID)

	if err := f.store.RemoveMember(ctx, group.ID, bob.ID); err != nil {
		t.Fatalf("RemoveMember() failed: %v", err)
	}
	f.hub.Notify(ctx, Notification{
		Type: NotifyConversationUpdated, ConversationID: group.ID, UserIDs: []int64{bob.ID},
	})

	frame := waitForType(t, bobClient, ws.TypeConversationUpd, 2*time.Second)

	var payload ws.ConversationUpdatedPayload
	if err := json.Unmarshal(mustData(t, frame), &payload); err != nil {
		t.Fatalf("decode conversation.updated: %v", err)
	}
	for _, m := range payload.Conversation.Members {
		if m.UserID == bob.ID {
			t.Error("the removed user is still listed as a member")
		}
	}
}

// failingPersister refuses the first fails messages, as a full persistence
// queue does, and then behaves like recordingPersister.
type failingPersister struct {
	recordingPersister
	fails int
}

func (p *failingPersister) Enqueue(ctx context.Context, msg models.Message) (<-chan models.Message, error) {
	p.mu.Lock()
	if p.fails > 0 {
		p.fails--
		p.mu.Unlock()
		return nil, errors.New("store: persistence queue is full")
	}
	p.mu.Unlock()
	return p.recordingPersister.Enqueue(ctx, msg)
}

// TestRetryAfterEnqueueFailureIsStored proves a message the persistence
// queue refused is neither acknowledged on the retry from the dedup cache
// nor leaves a hole in the sequence.
func TestRetryAfterEnqueueFailureIsStored(t *testing.T) {
	f := newHubFixture(t)
	alice, _, conv := f.createUsers(t)

	persister := &failingPersister{fails: 1}
	h := New(f.hub.cfg, f.store, persister)
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); h.Run(ctx) }()
	defer func() { stop(); <-done }()

	f2 := &hubFixture{t: t, hub: h, store: f.store, ctx: ctx}
	aliceClient := f2.addClient(alice.ID)
	send := mustEnvelope(ws.TypeMessageSend, ws.SendPayload{
		ClientID: "retry-me", ConversationID: conv.ID, Body: "hello",
	})

	f2.submit(aliceClient, send)
	waitForType(t, aliceClient, ws.TypeError, 2*time.Second)

	f2.submit(aliceClient, send)
	frame := waitForType(t, aliceClient, ws.TypeMessageAck, 2*time.Second)
	var ack ws.AckPayload
	if err := json.Unmarshal(mustData(t, frame), &ack); err != nil {
		t.Fatalf("decode message.ack: %v", err)
	}
	if persister.count() != 1 {
		t.Fatalf("retry was not handed to the persister: %d messages", persister.count())
	}
	if ack.Sequence != 1 || ack.ID == 0 {
		t.Errorf("ack after retry: got sequence %d, id %d; want sequence 1 with an id", ack.Sequence, ack.ID)
	}
}
