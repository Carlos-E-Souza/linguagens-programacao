package hub

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/ws"
)

// TestShutdownNotifiesConnectedClients covers the graceful-shutdown
// promise to the client: it must be told the server is going away so it
// knows to reconnect, rather than seeing an unexplained socket close.
func TestShutdownNotifiesConnectedClients(t *testing.T) {
	f := newHubFixture(t)
	alice, bob, _ := f.createUsers(t)

	aliceClient := f.addClient(alice.ID)
	_ = f.addClient(bob.ID)

	// Stopping the hub is what triggers the shutdown notice.
	f.stop()

	frame := waitForType(t, aliceClient, ws.TypeServerShutdown, 2*time.Second)

	var env ws.Envelope
	if err := json.Unmarshal(frame, &env); err != nil {
		t.Fatalf("shutdown frame is not a valid envelope: %v", err)
	}
	if got, want := env.Type, ws.TypeServerShutdown; got != want {
		t.Errorf("frame type: got %q, want %q", got, want)
	}
}

// TestShutdownEmptiesTheHub checks that no connection survives shutdown,
// which is what lets the process exit without leaking goroutines.
func TestShutdownEmptiesTheHub(t *testing.T) {
	f := newHubFixture(t)
	alice, bob, _ := f.createUsers(t)

	f.addClient(alice.ID)
	f.addClient(bob.ID)

	if got := len(f.hub.OnlineUsers()); got != 2 {
		t.Fatalf("online users before shutdown: got %d, want 2", got)
	}

	f.stop()

	if got := len(f.hub.OnlineUsers()); got != 0 {
		t.Errorf("online users after shutdown: got %d, want 0", got)
	}
}

// TestStopIsSafeWithoutClients guards against a shutdown path that only
// works when somebody is connected.
func TestStopIsSafeWithoutClients(t *testing.T) {
	f := newHubFixture(t)
	f.createUsers(t)

	f.stop()

	if got := len(f.hub.OnlineUsers()); got != 0 {
		t.Errorf("online users: got %d, want 0", got)
	}
}

// TestMultipleConnectionsPerUser covers the multi-device case: the hub
// keys clients per user, so a second connection must not evict the first,
// and presence must only flip when the last one goes.
func TestMultipleConnectionsPerUser(t *testing.T) {
	f := newHubFixture(t)
	alice, _, _ := f.createUsers(t)

	first := f.addClient(alice.ID)
	second := f.addClient(alice.ID)

	if !f.hub.IsOnline(alice.ID) {
		t.Fatal("user should be online with two connections")
	}

	// Closing one connection must leave the user online.
	first.close()
	f.hub.Unregister(first.Client)

	time.Sleep(100 * time.Millisecond)

	if !f.hub.IsOnline(alice.ID) {
		t.Error("user must stay online while a second connection remains")
	}

	// Closing the last one takes them offline.
	second.close()
	f.hub.Unregister(second.Client)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !f.hub.IsOnline(alice.ID) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Error("user should be offline once every connection is gone")
}

// TestSlowClientIsDisconnected proves the backpressure rule: a client
// that never drains its queue is dropped instead of blocking the hub.
func TestSlowClientIsDisconnected(t *testing.T) {
	f := newHubFixture(t)
	alice, bob, conv := f.createUsers(t)

	// Register bob without the pump, so nobody consumes his queue.
	slow, slowCtx := newClient(bob.ID, nil, 2)
	f.hub.Register(slow)
	defer slowCtx.Done()

	aliceClient := f.addClient(alice.ID)

	// Wait for the registration to be processed.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !f.hub.IsOnline(bob.ID) {
		time.Sleep(time.Millisecond)
	}

	// Fill the queue past its capacity.
	for i := 0; i < 10; i++ {
		f.submit(aliceClient, mustEnvelope(ws.TypeMessageSend, ws.SendPayload{
			ClientID:       "flood-" + string(rune('a'+i)),
			ConversationID: conv.ID,
			Body:           "flood",
		}))
	}

	// The hub must keep serving alice while dropping bob.
	waitForType(t, aliceClient, ws.TypeMessageAck, 3*time.Second)

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !f.hub.IsOnline(bob.ID) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Error("the slow client should have been disconnected")
}

// TestSyncSendsMissedMessages covers the reconnect backfill: messages
// written while the client was away must arrive, followed by sync.end.
func TestSyncSendsMissedMessages(t *testing.T) {
	f := newHubFixture(t)
	alice, bob, conv := f.createUsers(t)

	// Alice sends two messages while bob is offline.
	aliceClient := f.addClient(alice.ID)

	for i := 1; i <= 2; i++ {
		f.submit(aliceClient, mustEnvelope(ws.TypeMessageSend, ws.SendPayload{
			ClientID:       "offline-" + string(rune('0'+i)),
			ConversationID: conv.ID,
			Body:           "while you were away",
		}))
		waitForType(t, aliceClient, ws.TypeMessageAck, 2*time.Second)
	}

	// Persist them so the sync query can find them.
	if err := f.flushMessages(conv); err != nil {
		t.Fatalf("could not seed the history: %v", err)
	}

	bobClient := f.addClient(bob.ID)

	f.submit(bobClient, mustEnvelope(ws.TypeSync, ws.SyncPayload{
		Conversations: []ws.ConversationSequence{
			{ConversationID: conv.ID, LastSequence: 0},
		},
	}))

	// Two message.new frames, then the terminator.
	waitForType(t, bobClient, ws.TypeMessageNew, 3*time.Second)
	waitForType(t, bobClient, ws.TypeSyncEnd, 3*time.Second)
}
