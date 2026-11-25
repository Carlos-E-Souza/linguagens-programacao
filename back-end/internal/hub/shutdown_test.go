package hub

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/ws"
)

// TestShutdownNotifiesConnectedClients cobre a promessa do desligamento
// gracioso ao cliente: ele precisa ser avisado de que o servidor está
// saindo para saber que deve reconectar, em vez de ver o socket fechar
// sem explicação.
func TestShutdownNotifiesConnectedClients(t *testing.T) {
	f := newHubFixture(t)
	alice, bob, _ := f.createUsers(t)

	aliceClient := f.addClient(alice.ID)
	_ = f.addClient(bob.ID)

	// Parar o hub é o que dispara o aviso de desligamento.
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

// TestShutdownEmptiesTheHub confere que nenhuma conexão sobrevive ao
// desligamento, o que permite ao processo sair sem vazar goroutines.
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

// TestStopIsSafeWithoutClients protege contra um caminho de desligamento
// que só funcione quando há alguém conectado.
func TestStopIsSafeWithoutClients(t *testing.T) {
	f := newHubFixture(t)
	f.createUsers(t)

	f.stop()

	if got := len(f.hub.OnlineUsers()); got != 0 {
		t.Errorf("online users: got %d, want 0", got)
	}
}

// TestMultipleConnectionsPerUser cobre o caso de vários dispositivos: o
// hub indexa os clientes por usuário, então uma segunda conexão não pode
// expulsar a primeira, e a presença só muda quando a última sai.
func TestMultipleConnectionsPerUser(t *testing.T) {
	f := newHubFixture(t)
	alice, _, _ := f.createUsers(t)

	first := f.addClient(alice.ID)
	second := f.addClient(alice.ID)

	if !f.hub.IsOnline(alice.ID) {
		t.Fatal("user should be online with two connections")
	}

	// Fechar uma conexão precisa manter o usuário online.
	first.close()
	f.hub.Unregister(first.Client)

	time.Sleep(100 * time.Millisecond)

	if !f.hub.IsOnline(alice.ID) {
		t.Error("user must stay online while a second connection remains")
	}

	// Fechar a última o deixa offline.
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

// TestSlowClientIsDisconnected prova a regra de backpressure: um cliente
// que nunca drena a fila é derrubado em vez de bloquear o hub.
func TestSlowClientIsDisconnected(t *testing.T) {
	f := newHubFixture(t)
	alice, bob, conv := f.createUsers(t)

	// Registra bob sem o bombeamento, para ninguém consumir a fila dele.
	slow, slowCtx := newClient(bob.ID, nil, 2)
	f.hub.Register(slow)
	defer slowCtx.Done()

	aliceClient := f.addClient(alice.ID)

	// Espera o registro ser processado.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !f.hub.IsOnline(bob.ID) {
		time.Sleep(time.Millisecond)
	}

	// Enche a fila além da capacidade.
	for i := 0; i < 10; i++ {
		f.submit(aliceClient, mustEnvelope(ws.TypeMessageSend, ws.SendPayload{
			ClientID:       "flood-" + string(rune('a'+i)),
			ConversationID: conv.ID,
			Body:           "flood",
		}))
	}

	// O hub precisa continuar atendendo alice enquanto derruba bob.
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

// TestSyncSendsMissedMessages cobre o preenchimento na reconexão: as
// mensagens gravadas enquanto o cliente estava fora precisam chegar,
// seguidas de sync.end.
func TestSyncSendsMissedMessages(t *testing.T) {
	f := newHubFixture(t)
	alice, bob, conv := f.createUsers(t)

	// Alice envia duas mensagens enquanto bob está offline.
	aliceClient := f.addClient(alice.ID)

	for i := 1; i <= 2; i++ {
		f.submit(aliceClient, mustEnvelope(ws.TypeMessageSend, ws.SendPayload{
			ClientID:       "offline-" + string(rune('0'+i)),
			ConversationID: conv.ID,
			Body:           "while you were away",
		}))
		waitForType(t, aliceClient, ws.TypeMessageAck, 2*time.Second)
	}

	// Grava as mensagens para a consulta de sync encontrá-las.
	if err := f.flushMessages(conv); err != nil {
		t.Fatalf("could not seed the history: %v", err)
	}

	bobClient := f.addClient(bob.ID)

	f.submit(bobClient, mustEnvelope(ws.TypeSync, ws.SyncPayload{
		Conversations: []ws.ConversationSequence{
			{ConversationID: conv.ID, LastSequence: 0},
		},
	}))

	// Dois frames message.new, depois o terminador.
	waitForType(t, bobClient, ws.TypeMessageNew, 3*time.Second)
	waitForType(t, bobClient, ws.TypeSyncEnd, 3*time.Second)
}
