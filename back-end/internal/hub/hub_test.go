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

// recordingPersister captura as mensagens que o hub entrega e responde com
// uma linha gravada sintética, para que um teste confira a persistência e
// o ACK com ID real sem ir ao banco.
type recordingPersister struct {
	mu       sync.Mutex
	messages []models.Message
	nextID   int64
}

func (r *recordingPersister) Enqueue(ctx context.Context, msg models.Message) (<-chan models.Message, error) {
	r.mu.Lock()
	r.nextID++
	msg.ID = r.nextID
	r.messages = append(r.messages, msg)
	r.mu.Unlock()

	// Com buffer e já preenchido: o worker entregaria de forma assíncrona,
	// mas um dublê de teste pode responder na hora.
	stored := make(chan models.Message, 1)
	stored <- msg

	return stored, nil
}

func (r *recordingPersister) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.messages)
}

// hubFixture monta um hub com banco real mas sem rede, para que a lógica
// de roteamento seja testada de forma determinística.
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

// fakeClient é um cliente conectado sem socket: ele se registra no hub e
// coleta os frames que teriam sido escritos.
type fakeClient struct {
	*Client
	inbox chan []byte

	// pending guarda frames recebidos mas ainda não pedidos, para que um
	// helper procure um frame posterior sem perder um anterior.
	pendingMu sync.Mutex
	pending   [][]byte
}

// keep guarda um frame recebido mas ainda não reivindicado.
func (c *fakeClient) keep(frame []byte) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	c.pending = append(c.pending, frame)
}

// claim remove e devolve o primeiro frame guardado do tipo informado.
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

// addClient registra um cliente inventado para o usuário e bombeia a fila de saída dele.
func (f *hubFixture) addClient(userID int64) *fakeClient {
	f.t.Helper()

	client, clientCtx := newClient(userID, nil, 64)
	fc := &fakeClient{Client: client, inbox: make(chan []byte, 64)}

	// Faz o papel da goroutine de escrita: copia os frames enfileirados
	// para a caixa de entrada, para o teste poder inspecioná-los.
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

	// Espera o hub processar o registro; senão a primeira mensagem pode
	// ser roteada antes de o cliente ser conhecido.
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

// submit enfileira um frame como se tivesse chegado do cliente.
func (f *hubFixture) submit(c *fakeClient, env ws.Envelope) {
	f.t.Helper()

	if err := f.hub.Submit(f.ctx, c.Client, env, 0); err != nil {
		f.t.Fatalf("Submit() failed: %v", err)
	}
}

// createUsers cadastra dois usuários e uma conversa individual entre eles.
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

// flushMessages grava no store real as mensagens coletadas pelo persister
// de gravação, fazendo o papel do worker pool de persistência. É o que
// permite a um teste de sync ler um histórico de volta.
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

// envelopeType devolve o tipo de envelope de um frame bruto, ou uma
// string vazia quando o frame não pode ser decodificado.
//
// De propósito não recebe *testing.T: claim o chama segurando um mutex, e
// um t.Fatalf ali deixaria a trava presa.
func envelopeType(frame []byte) string {
	var env ws.Envelope
	if err := json.Unmarshal(frame, &env); err != nil {
		return ""
	}
	return env.Type
}

// decodeType é o invólucro para os testes que falha de forma visível com entrada inválida.
func decodeType(t *testing.T, frame []byte) string {
	t.Helper()

	kind := envelopeType(frame)
	if kind == "" {
		t.Fatalf("frame is not a valid envelope: %s", frame)
	}
	return kind
}

// waitForType devolve o primeiro frame do tipo desejado, guardando todos
// os outros para que uma busca posterior ainda os encontre.
//
// Frames nunca são descartados: um teste que espera message.new e depois
// sync.end perderia o terminador se ele chegasse primeiro.
func waitForType(t *testing.T, c *fakeClient, want string, timeout time.Duration) []byte {
	t.Helper()

	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		// Confere o que já está guardado antes de esperar por mais.
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
	// O ACK precisa trazer o ID atribuído pelo banco, não zero: o cliente
	// grava esse valor no cache e o usa para casar os frames message.new,
	// então um zero corromperia esse mapeamento.
	if ack.ID == 0 {
		t.Error("ack id must be the stored message id, got 0")
	}
	if ack.CreatedAt == "" {
		t.Error("ack created_at must be set")
	}

	// Bob precisa receber a mensagem.
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

	// E ela precisa ter sido entregue ao pool de persistência.
	if got, want := f.persister.count(), 1; got != want {
		t.Errorf("persisted messages: got %d, want %d", got, want)
	}
}

// TestSendMessageDeduplicatesByClientID é a garantia de que depende o
// fluxo de reenvio: o mesmo client_id não pode criar uma segunda mensagem.
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
	if a.ID != b.ID {
		t.Errorf("duplicate got a different id: %d then %d", a.ID, b.ID)
	}
	if got, want := f.persister.count(), 1; got != want {
		t.Errorf("persisted messages: got %d, want %d", got, want)
	}
}

// TestSendToForeignConversationIsRejected cobre o caminho de autorização.
func TestSendToForeignConversationIsRejected(t *testing.T) {
	f := newHubFixture(t)
	alice, bob, _ := f.createUsers(t)

	// Uma conversa entre alice e bob da qual carol não participa.
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

// TestSendValidationErrors cobre as rejeições baratas.
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

// TestSendRejectsBodyOverLimit protege o limite de tamanho da mensagem.
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

// TestUnknownTypeIsRejected impede que um erro de digitação no cliente
// falhe em silêncio.
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

// TestSequenceIncreasesAcrossMessages confere a ordenação dentro de uma
// conversa.
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

// TestPresenceBroadcastOnConnect confere que um contato fica sabendo
// quando um usuário entra.
func TestPresenceBroadcastOnConnect(t *testing.T) {
	f := newHubFixture(t)
	alice, bob, _ := f.createUsers(t)

	bobClient := f.addClient(bob.ID)
	// Deixa o registro de bob assentar antes de alice conectar.
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

// TestIsOnlineTracksConnections cobre a consulta de presença usada pela
// API REST.
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

// mustData extrai o campo data de um envelope.
func mustData(t *testing.T, frame []byte) []byte {
	t.Helper()

	var env ws.Envelope
	if err := json.Unmarshal(frame, &env); err != nil {
		t.Fatalf("frame is not a valid envelope: %v", err)
	}
	return env.Data
}

// clientIDFor monta um client ID estável para a n-ésima mensagem.
func clientIDFor(n int) string {
	return "client-" + string(rune('0'+n))
}
