package hub

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/config"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/db"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/ws"
)

// recordingPersister captures messages the hub hands over, so a test can
// assert on persistence without a real database round trip through the
// worker pool.
type recordingPersister struct {
	mu       sync.Mutex
	messages []models.Message
	nextID   int64
}

func (r *recordingPersister) Enqueue(ctx context.Context, msg models.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++
	msg.ID = r.nextID
	r.messages = append(r.messages, msg)

	return nil
}

func (r *recordingPersister) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.messages)
}

// hubFixture assembles a hub with a real database but no network, so the
// routing logic can be tested deterministically.
type hubFixture struct {
	t         *testing.T
	hub       *Hub
	store     *db.Store
	persister *recordingPersister
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
}

func newHubFixture(t *testing.T) *hubFixture {
	t.Helper()

	handle, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatalf("db.Open() failed: %v", err)
	}
	t.Cleanup(func() { handle.Close() })

	if err := db.Migrate(context.Background(), handle); err != nil {
		t.Fatalf("db.Migrate() failed: %v", err)
	}

	cfg := config.Config{
		ClientQueueSize:   64,
		MessageLimitChars: 4000,
		TypingCooldown:    0,
	}

	store := db.NewStore(handle)
	persister := &recordingPersister{}
	h := New(cfg, store, persister)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)
		h.Run(ctx)
	}()

	f := &hubFixture{
		t: t, hub: h, store: store, persister: persister,
		ctx: ctx, cancel: cancel, done: done,
	}
	t.Cleanup(f.stop)

	return f
}

func (f *hubFixture) stop() {
	f.cancel()
	select {
	case <-f.done:
	case <-time.After(3 * time.Second):
		f.t.Error("hub did not stop within the timeout")
	}
}

// fakeClient is a connected client with no socket: it registers with the
// hub and collects the frames that would have been written.
type fakeClient struct {
	*Client
	inbox chan []byte

	// pending holds frames that were received but not yet asked for, so
	// a helper can look for a later frame without losing an earlier one.
	pendingMu sync.Mutex
	pending   [][]byte
}

// keep stores a frame that was received but not claimed yet.
func (c *fakeClient) keep(frame []byte) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	c.pending = append(c.pending, frame)
}

// claim removes and returns the first buffered frame of the given type.
func (c *fakeClient) claim(want string) ([]byte, bool) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()

	for i, frame := range c.pending {
		if envelopeType(frame) == want {
			c.pending = append(c.pending[:i], c.pending[i+1:]...)
			return frame, true
		}
	}

	return nil, false
}

func (c *fakeClient) drain(timeout time.Duration) [][]byte {
	deadline := time.After(timeout)
	var frames [][]byte

	for {
		select {
		case frame := <-c.inbox:
			frames = append(frames, frame)
		case <-deadline:
			return frames
		}
	}
}

// addClient registers a user made-up client and pumps its outgoing queue.
func (f *hubFixture) addClient(userID int64) *fakeClient {
	f.t.Helper()

	client, clientCtx := newClient(userID, nil, 64)
	fc := &fakeClient{Client: client, inbox: make(chan []byte, 64)}

	// Stand in for the write goroutine: copy queued frames into the inbox
	// so the test can inspect them.
	go func() {
		for {
			frame, ok := client.Next(clientCtx)
			if !ok {
				close(fc.inbox)
				return
			}
			select {
			case fc.inbox <- frame:
			case <-clientCtx.Done():
				close(fc.inbox)
				return
			}
		}
	}()

	f.hub.Register(client)

	// Wait until the hub has processed the registration, otherwise the
	// first message may be routed before the client is known.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if f.hub.IsOnline(userID) {
			return fc
		}
		time.Sleep(time.Millisecond)
	}

	f.t.Fatalf("client %d never became online", userID)
	return nil
}

// submit queues a frame as if it arrived from the client.
func (f *hubFixture) submit(c *fakeClient, env ws.Envelope) {
	f.t.Helper()

	if err := f.hub.Submit(f.ctx, c.Client, env, 0); err != nil {
		f.t.Fatalf("Submit() failed: %v", err)
	}
}

// createUsers registers two users and a direct conversation between them.
func (f *hubFixture) createUsers(t *testing.T) (models.User, models.User, models.Conversation) {
	t.Helper()

	ctx := context.Background()

	alice, err := f.store.CreateUser(ctx, "alice", "Alice", "hash")
	if err != nil {
		t.Fatalf("CreateUser(alice) failed: %v", err)
	}
	bob, err := f.store.CreateUser(ctx, "bob", "Bob", "hash")
	if err != nil {
		t.Fatalf("CreateUser(bob) failed: %v", err)
	}

	conv, err := f.store.CreateConversation(ctx,
		models.ConversationDirect, "", alice.ID, []int64{alice.ID, bob.ID}, nil)
	if err != nil {
		t.Fatalf("CreateConversation() failed: %v", err)
	}

	return alice, bob, conv
}

// flushMessages writes the messages collected by the recording persister
// into the real store, standing in for the persistence worker pool. It is
// what lets a sync test read back a history.
func (f *hubFixture) flushMessages(conv models.Conversation) error {
	f.persister.mu.Lock()
	batch := make([]models.Message, len(f.persister.messages))
	copy(batch, f.persister.messages)
	f.persister.mu.Unlock()

	if len(batch) == 0 {
		return nil
	}

	_, err := f.store.InsertMessages(context.Background(), batch)
	return err
}

// envelopeType returns the envelope type of a raw frame, or an empty
// string when the frame cannot be decoded.
//
// It deliberately does not take *testing.T: claim calls it while holding
// a mutex, and a t.Fatalf there would leave the lock held.
func envelopeType(frame []byte) string {
	var env ws.Envelope
	if err := json.Unmarshal(frame, &env); err != nil {
		return ""
	}
	return env.Type
}

// decodeType is the test-facing wrapper that fails loudly on bad input.
func decodeType(t *testing.T, frame []byte) string {
	t.Helper()

	kind := envelopeType(frame)
	if kind == "" {
		t.Fatalf("frame is not a valid envelope: %s", frame)
	}
	return kind
}

// waitForType returns the first frame of the wanted type, keeping every
// other frame so a later lookup can still find it.
//
// Frames are never discarded: a test that waits for message.new and then
// for sync.end would otherwise lose the terminator when it happened to
// arrive first.
func waitForType(t *testing.T, c *fakeClient, want string, timeout time.Duration) []byte {
	t.Helper()

	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		// Check what is already buffered before waiting for more.
		if frame, ok := c.claim(want); ok {
			return frame
		}

		for _, frame := range c.drain(50 * time.Millisecond) {
			c.keep(frame)
		}
	}

	t.Fatalf("no %q frame received within %s", want, timeout)
	return nil
}

func TestSendMessageIsAcknowledgedAndDelivered(t *testing.T) {
	f := newHubFixture(t)
	alice, bob, conv := f.createUsers(t)

	aliceClient := f.addClient(alice.ID)
	bobClient := f.addClient(bob.ID)

	f.submit(aliceClient, mustEnvelope(ws.TypeMessageSend, ws.SendPayload{
		ClientID:       "client-1",
		ConversationID: conv.ID,
		Body:           "hello bob",
	}))

	ackFrame := waitForType(t, aliceClient, ws.TypeMessageAck, 2*time.Second)
	var ack ws.AckPayload
	if err := json.Unmarshal(mustData(t, ackFrame), &ack); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if got, want := ack.ClientID, "client-1"; got != want {
		t.Errorf("ack client_id: got %q, want %q", got, want)
	}
	if got, want := ack.Sequence, int64(1); got != want {
		t.Errorf("ack sequence: got %d, want %d", got, want)
	}

	// Bob must receive the message.
	newFrame := waitForType(t, bobClient, ws.TypeMessageNew, 2*time.Second)
	var msg models.Message
	if err := json.Unmarshal(mustData(t, newFrame), &msg); err != nil {
		t.Fatalf("decode message.new: %v", err)
	}
	if got, want := msg.Body, "hello bob"; got != want {
		t.Errorf("body: got %q, want %q", got, want)
	}
	if got, want := msg.SenderID, alice.ID; got != want {
		t.Errorf("sender: got %d, want %d", got, want)
	}

	// And it must have been handed to the persistence pool.
	if got, want := f.persister.count(), 1; got != want {
		t.Errorf("persisted messages: got %d, want %d", got, want)
	}
}

// TestSendMessageDeduplicatesByClientID is the guarantee the resend flow
// depends on: the same client_id must not create a second message.
func TestSendMessageDeduplicatesByClientID(t *testing.T) {
	f := newHubFixture(t)
	alice, _, conv := f.createUsers(t)

	aliceClient := f.addClient(alice.ID)

	payload := ws.SendPayload{ClientID: "same-id", ConversationID: conv.ID, Body: "once"}

	f.submit(aliceClient, mustEnvelope(ws.TypeMessageSend, payload))
	first := waitForType(t, aliceClient, ws.TypeMessageAck, 2*time.Second)

	f.submit(aliceClient, mustEnvelope(ws.TypeMessageSend, payload))
	second := waitForType(t, aliceClient, ws.TypeMessageAck, 2*time.Second)

	var a, b ws.AckPayload
	_ = json.Unmarshal(mustData(t, first), &a)
	_ = json.Unmarshal(mustData(t, second), &b)

	if a.Sequence != b.Sequence {
		t.Errorf("duplicate got a new sequence: %d then %d", a.Sequence, b.Sequence)
	}
	if got, want := f.persister.count(), 1; got != want {
		t.Errorf("persisted messages: got %d, want %d", got, want)
	}
}

// TestSendToForeignConversationIsRejected covers the authorisation path.
func TestSendToForeignConversationIsRejected(t *testing.T) {
	f := newHubFixture(t)
	alice, bob, _ := f.createUsers(t)

	// A conversation between alice and bob that carol is not part of.
	carol, err := f.store.CreateUser(context.Background(), "carol", "Carol", "hash")
	if err != nil {
		t.Fatalf("CreateUser(carol) failed: %v", err)
	}

	conv, err := f.store.CreateConversation(context.Background(),
		models.ConversationDirect, "", alice.ID, []int64{alice.ID, bob.ID}, nil)
	if err != nil {
		t.Fatalf("CreateConversation() failed: %v", err)
	}

	carolClient := f.addClient(carol.ID)

	f.submit(carolClient, mustEnvelope(ws.TypeMessageSend, ws.SendPayload{
		ClientID:       "intruder",
		ConversationID: conv.ID,
		Body:           "let me in",
	}))

	frame := waitForType(t, carolClient, ws.TypeError, 2*time.Second)

	var errPayload ws.ErrorPayload
	if err := json.Unmarshal(mustData(t, frame), &errPayload); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if got, want := errPayload.Code, "not_a_member"; got != want {
		t.Errorf("error code: got %q, want %q", got, want)
	}
	if got, want := f.persister.count(), 0; got != want {
		t.Errorf("persisted messages: got %d, want %d", got, want)
	}
}

// TestSendValidationErrors covers the cheap rejections.
func TestSendValidationErrors(t *testing.T) {
	f := newHubFixture(t)
	alice, _, conv := f.createUsers(t)

	cases := []struct {
		name    string
		payload ws.SendPayload
		want    string
	}{
		{
			"empty body",
			ws.SendPayload{ClientID: "c1", ConversationID: conv.ID, Body: "   "},
			"empty_body",
		},
		{
			"missing client id",
			ws.SendPayload{ConversationID: conv.ID, Body: "hi"},
			"missing_client_id",
		},
		{
			"missing conversation",
			ws.SendPayload{ClientID: "c2", Body: "hi"},
			"invalid_conversation_id",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := f.addClient(alice.ID)
			f.submit(client, mustEnvelope(ws.TypeMessageSend, tc.payload))

			frame := waitForType(t, client, ws.TypeError, 2*time.Second)

			var errPayload ws.ErrorPayload
			if err := json.Unmarshal(mustData(t, frame), &errPayload); err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if got := errPayload.Code; got != tc.want {
				t.Errorf("error code: got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSendRejectsBodyOverLimit guards the message size cap.
func TestSendRejectsBodyOverLimit(t *testing.T) {
	f := newHubFixture(t)
	alice, _, conv := f.createUsers(t)

	client := f.addClient(alice.ID)

	long := make([]rune, f.hub.cfg.MessageLimitChars+1)
	for i := range long {
		long[i] = 'a'
	}

	f.submit(client, mustEnvelope(ws.TypeMessageSend, ws.SendPayload{
		ClientID: "too-long", ConversationID: conv.ID, Body: string(long),
	}))

	frame := waitForType(t, client, ws.TypeError, 2*time.Second)

	var errPayload ws.ErrorPayload
	if err := json.Unmarshal(mustData(t, frame), &errPayload); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if got, want := errPayload.Code, "body_too_long"; got != want {
		t.Errorf("error code: got %q, want %q", got, want)
	}
}

// TestUnknownTypeIsRejected keeps a typo in the client from failing
// silently.
func TestUnknownTypeIsRejected(t *testing.T) {
	f := newHubFixture(t)
	alice, _, _ := f.createUsers(t)

	client := f.addClient(alice.ID)
	f.submit(client, ws.Envelope{Type: "nonsense"})

	frame := waitForType(t, client, ws.TypeError, 2*time.Second)

	var errPayload ws.ErrorPayload
	if err := json.Unmarshal(mustData(t, frame), &errPayload); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if got, want := errPayload.Code, "unknown_type"; got != want {
		t.Errorf("error code: got %q, want %q", got, want)
	}
}

// TestSequenceIncreasesAcrossMessages checks ordering within a
// conversation.
func TestSequenceIncreasesAcrossMessages(t *testing.T) {
	f := newHubFixture(t)
	alice, _, conv := f.createUsers(t)

	client := f.addClient(alice.ID)

	for i := 1; i <= 3; i++ {
		f.submit(client, mustEnvelope(ws.TypeMessageSend, ws.SendPayload{
			ClientID:       clientIDFor(i),
			ConversationID: conv.ID,
			Body:           "message",
		}))

		frame := waitForType(t, client, ws.TypeMessageAck, 2*time.Second)

		var ack ws.AckPayload
		if err := json.Unmarshal(mustData(t, frame), &ack); err != nil {
			t.Fatalf("decode ack: %v", err)
		}
		if got, want := ack.Sequence, int64(i); got != want {
			t.Errorf("sequence %d: got %d, want %d", i, got, want)
		}
	}
}

// TestPresenceBroadcastOnConnect checks that a contact learns when a user
// comes online.
func TestPresenceBroadcastOnConnect(t *testing.T) {
	f := newHubFixture(t)
	alice, bob, _ := f.createUsers(t)

	bobClient := f.addClient(bob.ID)
	// Let bob's own registration settle before alice connects.
	time.Sleep(50 * time.Millisecond)

	aliceClient := f.addClient(alice.ID)

	frame := waitForType(t, bobClient, ws.TypePresence, 3*time.Second)

	var presence ws.PresencePayload
	if err := json.Unmarshal(mustData(t, frame), &presence); err != nil {
		t.Fatalf("decode presence: %v", err)
	}
	if got, want := presence.UserID, alice.ID; got != want {
		t.Errorf("presence user: got %d, want %d", got, want)
	}
	if !presence.Online {
		t.Error("expected online: true")
	}

	_ = aliceClient
}

// TestTypingIsForwardedToOtherMembersOnly covers the fan-out rule.
func TestTypingIsForwardedToOtherMembersOnly(t *testing.T) {
	f := newHubFixture(t)
	alice, bob, conv := f.createUsers(t)

	aliceClient := f.addClient(alice.ID)
	bobClient := f.addClient(bob.ID)

	f.submit(aliceClient, mustEnvelope(ws.TypeTyping, ws.TypingPayload{
		ConversationID: conv.ID, Active: true,
	}))

	frame := waitForType(t, bobClient, ws.TypeTyping, 2*time.Second)

	var typing ws.TypingEvent
	if err := json.Unmarshal(mustData(t, frame), &typing); err != nil {
		t.Fatalf("decode typing: %v", err)
	}
	if got, want := typing.UserID, alice.ID; got != want {
		t.Errorf("typing user: got %d, want %d", got, want)
	}
	if !typing.Active {
		t.Error("expected active: true")
	}

	// Alice must not receive her own typing event.
	for _, frame := range aliceClient.drain(300 * time.Millisecond) {
		if decodeType(t, frame) == ws.TypeTyping {
			t.Error("sender received their own typing event")
		}
	}
}

// TestIsOnlineTracksConnections covers the presence query used by the
// REST API.
func TestIsOnlineTracksConnections(t *testing.T) {
	f := newHubFixture(t)
	alice, _, _ := f.createUsers(t)

	if f.hub.IsOnline(alice.ID) {
		t.Fatal("user should be offline before connecting")
	}

	client := f.addClient(alice.ID)

	if !f.hub.IsOnline(alice.ID) {
		t.Error("user should be online after connecting")
	}

	client.close()
	f.hub.Unregister(client.Client)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !f.hub.IsOnline(alice.ID) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Error("user should be offline after disconnecting")
}

// mustData extracts the data field of an envelope.
func mustData(t *testing.T, frame []byte) []byte {
	t.Helper()

	var env ws.Envelope
	if err := json.Unmarshal(frame, &env); err != nil {
		t.Fatalf("frame is not a valid envelope: %v", err)
	}
	return env.Data
}

// clientIDFor builds a stable client ID for the nth message.
func clientIDFor(n int) string {
	return "client-" + string(rune('0'+n))
}
