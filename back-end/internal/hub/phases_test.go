package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/store"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/ws"
)

// TestReceiptsAreBroadcastOnceAndPersisted cobre a Fase 7: uma confirmação
// de entrega ou leitura chega aos outros membros exatamente uma vez e move
// a marca do membro no banco.
func TestReceiptsAreBroadcastOnceAndPersisted(t *testing.T) {
	f := newHubFixture(t)
	alice, bob, conv := f.createUsers(t)

	aliceClient := f.addClient(alice.ID)
	bobClient := f.addClient(bob.ID)

	cases := []struct {
		kind   string
		status string
	}{
		{ws.TypeMessageDelivered, models.StatusDelivered},
		{ws.TypeMessageRead, models.StatusRead},
	}

	for i, tc := range cases {
		upTo := int64(i + 1)
		f.submit(bobClient, mustEnvelope(tc.kind, ws.ReadReceiptPayload{
			ConversationID: conv.ID, UpToSequence: upTo,
		}))

		frame := waitForType(t, aliceClient, ws.TypeMessageStatus, 2*time.Second)

		var status ws.StatusPayload
		if err := json.Unmarshal(mustData(t, frame), &status); err != nil {
			t.Fatalf("decode message.status: %v", err)
		}
		if status.UserID != bob.ID || status.UpToSequence != upTo || status.Status != tc.status {
			t.Errorf("%s: got %+v", tc.kind, status)
		}

		// Uma segunda cópia do mesmo status é a duplicata contra a qual
		// este teste protege.
		for _, extra := range aliceClient.drain(300 * time.Millisecond) {
			if decodeType(t, extra) == ws.TypeMessageStatus {
				t.Errorf("%s: message.status was delivered more than once", tc.kind)
			}
		}

		// Quem enviou a confirmação não pode recebê-la de volta.
		if _, ok := bobClient.claim(ws.TypeMessageStatus); ok {
			t.Errorf("%s: the member who confirmed received their own status", tc.kind)
		}
	}

	// A gravação da marca é assíncrona, então consulta até aparecer.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		members, err := f.store.ListMembers(context.Background(), conv.ID)
		if err != nil {
			t.Fatalf("ListMembers() failed: %v", err)
		}
		for _, m := range members {
			if m.UserID == bob.ID && m.LastDelivered == 2 && m.LastRead == 2 {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("bob's watermarks were not persisted (want last_delivered=2, last_read=2)")
}

// TestConcurrentSendsGetUniqueOrderedSequences cobre a Fase 5: muitas
// conexões enviando ao mesmo tempo para uma conversa ainda produzem as
// sequences 1..N sem buraco e sem repetição.
func TestConcurrentSendsGetUniqueOrderedSequences(t *testing.T) {
	f := newHubFixture(t)
	alice, bob, conv := f.createUsers(t)

	const perClient = 10
	senders := []*fakeClient{
		f.addClient(alice.ID),
		f.addClient(alice.ID),
		f.addClient(bob.ID),
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(senders)*perClient)
	for ci, c := range senders {
		wg.Add(1)
		go func(ci int, c *fakeClient) {
			defer wg.Done()
			for i := 0; i < perClient; i++ {
				env := mustEnvelope(ws.TypeMessageSend, ws.SendPayload{
					ClientID:       fmt.Sprintf("conc-%d-%d", ci, i),
					ConversationID: conv.ID,
					Body:           "concurrent",
				})
				if err := f.hub.Submit(f.ctx, c.Client, env, 0); err != nil {
					errs <- err
				}
			}
		}(ci, c)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Submit() failed: %v", err)
	}

	var sequences []int64
	for _, c := range senders {
		for i := 0; i < perClient; i++ {
			frame := waitForType(t, c, ws.TypeMessageAck, 3*time.Second)
			var ack ws.AckPayload
			if err := json.Unmarshal(mustData(t, frame), &ack); err != nil {
				t.Fatalf("decode ack: %v", err)
			}
			sequences = append(sequences, ack.Sequence)
		}
	}

	sort.Slice(sequences, func(i, j int) bool { return sequences[i] < sequences[j] })
	for i, seq := range sequences {
		if want := int64(i + 1); seq != want {
			t.Fatalf("sequences are not 1..%d without gaps or repeats: %v", len(senders)*perClient, sequences)
		}
	}
}

// TestReconnectSyncDeliversMissedMessagesOnce cobre a Fase 8: um membro
// que viu a mensagem 1, ficou offline e voltou recebe exatamente as
// mensagens depois da 1, em ordem, e então sync.end.
func TestReconnectSyncDeliversMissedMessagesOnce(t *testing.T) {
	f := newHubFixture(t)
	alice, bob, conv := f.createUsers(t)

	aliceClient := f.addClient(alice.ID)
	bobClient := f.addClient(bob.ID)

	send := func(n int) {
		f.submit(aliceClient, mustEnvelope(ws.TypeMessageSend, ws.SendPayload{
			ClientID:       fmt.Sprintf("reconnect-%d", n),
			ConversationID: conv.ID,
			Body:           fmt.Sprintf("message %d", n),
		}))
		waitForType(t, aliceClient, ws.TypeMessageAck, 2*time.Second)
	}

	send(1)
	waitForType(t, bobClient, ws.TypeMessageNew, 2*time.Second)

	// Bob cai.
	bobClient.close()
	f.hub.Unregister(bobClient.Client)

	send(2)
	send(3)

	if err := f.flushMessages(conv); err != nil {
		t.Fatalf("could not persist the history: %v", err)
	}

	bobAgain := f.addClient(bob.ID)
	f.submit(bobAgain, mustEnvelope(ws.TypeSync, ws.SyncPayload{
		Conversations: []ws.ConversationSequence{{ConversationID: conv.ID, LastSequence: 1}},
	}))

	waitForType(t, bobAgain, ws.TypeSyncEnd, 3*time.Second)

	var got []int64
	for {
		frame, ok := bobAgain.claim(ws.TypeMessageNew)
		if !ok {
			break
		}
		var msg models.Message
		if err := json.Unmarshal(mustData(t, frame), &msg); err != nil {
			t.Fatalf("decode message.new: %v", err)
		}
		got = append(got, msg.Sequence)
	}

	if fmt.Sprint(got) != fmt.Sprint([]int64{2, 3}) {
		t.Errorf("synced sequences: got %v, want [2 3]", got)
	}
}

// TestAcceptedMessagesSurviveShutdown cobre a Fase 10 com o pool de
// persistência real: toda mensagem confirmada está no banco depois que o
// hub para e o pool é encerrado, na mesma ordem usada por cmd/server.
func TestAcceptedMessagesSurviveShutdown(t *testing.T) {
	f := newHubFixture(t)
	alice, _, conv := f.createUsers(t)

	// Um intervalo de despacho lento faz as mensagens ainda estarem na
	// fila quando o desligamento começa, que é o caso que as perdia.
	pool := store.NewPool(f.store, store.Options{
		Workers:     2,
		QueueSize:   128,
		BatchSize:   1000,
		FlushEvery:  time.Hour,
		EnqueueWait: time.Second,
	})

	h := New(f.hub.cfg, f.store, pool)
	ctx, stopHub := context.WithCancel(context.Background())
	hubDone := make(chan struct{})
	go func() {
		defer close(hubDone)
		h.Run(ctx)
	}()

	client, _ := newClient(alice.ID, nil, 256)
	h.Register(client)

	const total = 50
	for i := 0; i < total; i++ {
		env := mustEnvelope(ws.TypeMessageSend, ws.SendPayload{
			ClientID:       fmt.Sprintf("shutdown-%d", i),
			ConversationID: conv.ID,
			Body:           "must survive",
		})
		if err := h.Submit(context.Background(), client, env, 0); err != nil {
			t.Fatalf("Submit() failed: %v", err)
		}
	}

	// Espera até toda mensagem ser aceita pelo hub, que é o momento em que
	// ela foi entregue e passada ao pool.
	accepted := 0
	deadline := time.After(3 * time.Second)
	for accepted < total {
		select {
		case frame := <-client.send:
			if envelopeType(frame) == ws.TypeMessageNew {
				accepted++
			}
		case <-deadline:
			t.Fatalf("only %d of %d messages were accepted", accepted, total)
		}
	}

	stopHub()
	<-hubDone

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("pool.Shutdown() failed: %v", err)
	}

	stored, err := f.store.ListMessages(context.Background(), conv.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListMessages() failed: %v", err)
	}
	if got := len(stored); got != total {
		t.Errorf("messages in the database after shutdown: got %d, want %d", got, total)
	}
}
