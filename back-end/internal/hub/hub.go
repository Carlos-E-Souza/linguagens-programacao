package hub

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/config"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/db"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/ws"
)

// Persister aceita uma mensagem para gravação assíncrona.
//
// O hub depende desta interface estreita em vez de store.Pool, para que os
// dois sejam testáveis de forma independente e o hub não possa bloquear
// numa chamada ao banco por acidente.
type Persister interface {
	// Enqueue aceita uma mensagem para gravação assíncrona e devolve um
	// canal que entrega a linha gravada, com o ID atribuído pelo banco,
	// depois que o lote é confirmado. O canal sempre recebe exatamente um
	// valor: uma Message zerada quando a linha não foi gravada.
	Enqueue(ctx context.Context, msg models.Message) (<-chan models.Message, error)
}

// Hub é dono de todo cliente conectado e roteia as mensagens entre eles.
//
// Modelo de concorrência, que é o coração deste pacote:
//
//   - Uma única goroutine (Run) altera o estado do hub. Como só ela
//     escreve, os mapas de participação não precisam de trava, e os
//     números de sequence podem ser atribuídos sem corrida.
//   - As outras goroutines (handlers HTTP, escritores das conexões) só
//     leem. Essas leituras são protegidas por um sync.RWMutex, que
//     permite muitos leitores simultâneos.
//   - Todo cliente tem um canal de saída com buffer, então o hub nunca
//     escreve num socket e nunca bloqueia por causa de um par lento.
type Hub struct {
	cfg       config.Config
	store     *db.Store
	persister Persister

	// clients é protegido por mu porque handlers HTTP e goroutines de
	// fundo o leem. Os mapas abaixo dele pertencem só ao laço Run e nunca
	// podem ser tocados por outra goroutine.
	mu      sync.RWMutex
	clients map[int64]map[*Client]struct{}
	members map[int64]map[int64]struct{} // conversation_id -> conjunto de user_id
	acks    map[ackKey]ws.AckPayload     // (sender_id, client_id) -> ack anterior
	seqs    map[int64]int64              // conversation_id -> última sequence atribuída

	// unsaved guarda as mensagens entregues ao pool de persistência que o
	// banco ainda não confirmou (conversation_id -> sequence -> mensagem).
	// Um sync o combina com a consulta ao banco, para que uma mensagem
	// aceita instantes antes de uma reconexão não falte no preenchimento.
	unsaved map[int64]map[int64]models.Message

	// Canais de entrada do laço Run. Só Run os lê.
	register   chan *Client
	unregister chan *Client

	// persisted informa que o pool terminou com uma mensagem (gravada ou
	// não). Permite ao laço enriquecer o cache de deduplicação e tirar a
	// mensagem de unsaved sem que a goroutine que espera mexa no estado do
	// hub.
	persisted chan persistedMsg

	// receipts alimenta a goroutine que grava as marcas de entrega/leitura
	// em lote. Só o laço envia nele, e o desligamento o fecha.
	receipts chan db.Receipt

	// drop é uma fila com buffer das conexões que o próprio hub decidiu
	// desconectar (clientes lentos). É separada de unregister para que o
	// laço possa enfileirar sem nunca bloquear em si mesmo, e tem buffer
	// para que uma rajada de quedas nunca trave a entrega.
	drop     chan *Client
	incoming chan inbound
	notify   chan Notification

	// done é fechado quando Run retorna, para que quem precisa passar
	// trabalho ao laço perceba que ele acabou em vez de bloquear para
	// sempre.
	done chan struct{}

	// wg acompanha as goroutines de longa duração para o desligamento esperá-las.
	wg sync.WaitGroup
}

// persistedMsg avisa o laço que o pool respondeu por uma mensagem.
// stored é false quando a linha não foi gravada (duplicata ou falha de
// gravação); o ack então não traz ID do banco.
type persistedMsg struct {
	key            ackKey
	ack            ws.AckPayload
	stored         bool
	conversationID int64
	sequence       int64
}

// ackKey identifica um ID de mensagem fornecido pelo cliente dentro de um remetente.
type ackKey struct {
	senderID int64
	clientID string
}

// inbound é um frame recebido de um cliente.
type inbound struct {
	client  *Client
	env     ws.Envelope
	rawSize int
}

// NotificationType distingue os eventos que a camada REST envia ao hub
// para que os clientes conectados saibam de mudanças feitas por HTTP.
type NotificationType string

const (
	// NotifyConversationUpdated avisa os membros de uma conversa que a
	// participação nela mudou.
	NotifyConversationUpdated NotificationType = "conversation_updated"
)

// Notification é um evento gerado fora do caminho WebSocket.
type Notification struct {
	Type           NotificationType
	ConversationID int64
	// UserIDs são destinatários extras que não são mais membros, como um
	// usuário que acabou de ser removido: eles ainda precisam da
	// atualização para saber que a conversa sumiu para eles.
	UserIDs []int64
}

// shutdownGrace é quanto tempo o hub espera o aviso de desligamento chegar
// aos clientes antes de fechar as conexões.
const shutdownGrace = 100 * time.Millisecond

// maxAckCacheEntries limita o cache de deduplicação. Um cliente que
// reconecta e reenvia faz isso rápido, então só as entradas recentes
// importam; o índice único do banco continua sendo a garantia real.
const maxAckCacheEntries = 4096

// Lote de confirmações: o gravador junta as confirmações por até
// receiptFlushEvery, ou até haver maxReceiptBatch membros pendentes, numa
// única transação.
const (
	receiptFlushEvery = 100 * time.Millisecond
	maxReceiptBatch   = 256
	receiptQueueSize  = 4096
)

// Códigos e motivos de close enviados quando o próprio hub encerra uma conexão.
const (
	closeSlowReason     = "client too slow"
	closeShutdownReason = "server shutting down"
)

// New monta um hub. O persister pode ser nil, e nesse caso as mensagens
// são entregues mas não gravadas (útil nos testes).
func New(cfg config.Config, store *db.Store, persister Persister) *Hub {
	return &Hub{
		cfg:        cfg,
		store:      store,
		persister:  persister,
		clients:    make(map[int64]map[*Client]struct{}),
		members:    make(map[int64]map[int64]struct{}),
		acks:       make(map[ackKey]ws.AckPayload),
		seqs:       make(map[int64]int64),
		unsaved:    make(map[int64]map[int64]models.Message),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		persisted:  make(chan persistedMsg, 256),
		receipts:   make(chan db.Receipt, receiptQueueSize),
		drop:       make(chan *Client, 64),
		incoming:   make(chan inbound, 256),
		notify:     make(chan Notification, 128),
		done:       make(chan struct{}),
	}
}

// Run é o laço de eventos do hub. Bloqueia até ctx ser cancelado.
//
// Nenhum handler mexe nos mapas diretamente: tudo chega como mensagem em
// um dos canais abaixo, o que serializa toda alteração por este único
// select.
func (h *Hub) Run(ctx context.Context) {
	slog.Info("hub started")

	if h.store != nil {
		h.wg.Add(1)
		go h.runReceiptWriter()
	}

	for {
		select {
		case <-ctx.Done():
			h.shutdown()
			close(h.done)
			return

		case client := <-h.register:
			h.handleRegister(client)

		case client := <-h.unregister:
			h.handleUnregister(client)

		case client := <-h.drop:
			h.handleUnregister(client)

		case pm := <-h.persisted:
			h.handlePersisted(pm)

		case in := <-h.incoming:
			h.handleIncoming(ctx, in)

		case n := <-h.notify:
			h.handleNotification(n)
		}
	}
}

// shutdown fecha todas as conexões e espera as goroutines.
func (h *Hub) shutdown() {
	h.mu.Lock()
	clients := make([]*Client, 0, len(h.clients))
	for _, set := range h.clients {
		for c := range set {
			clients = append(clients, c)
		}
	}
	h.clients = make(map[int64]map[*Client]struct{})
	h.mu.Unlock()

	// Avisa os clientes que o servidor está saindo antes de fechar as
	// filas deles, para que o frame seja de fato escrito. Sem isso o
	// cliente só veria o socket cair e teria de adivinhar se deve
	// reconectar.
	if frame, err := encodeFrame(ws.Envelope{Type: ws.TypeServerShutdown}); err == nil {
		for _, c := range clients {
			c.trySend(frame)
		}

		// Dá às goroutines de escrita um instante para despachar. É um
		// período de tolerância fixo, e não um wait group, porque um
		// cliente pode estar lento ou já ter sumido, e o desligamento não
		// pode travar por causa dele.
		time.Sleep(shutdownGrace)
	}

	for _, c := range clients {
		c.closeWith(websocket.StatusGoingAway, closeShutdownReason)
	}

	// O laço é o único que envia em receipts, e ele parou, então o canal
	// pode ser fechado: o gravador despacha o que tem e retorna.
	close(h.receipts)

	h.wg.Wait()
	slog.Info("hub stopped", "clients_notified", len(clients))
}

// Register adiciona uma conexão ao hub.
func (h *Hub) Register(client *Client) {
	select {
	case h.register <- client:
	case <-client.context().Done():
	}
}

// Unregister remove uma conexão do hub.
//
// Não pode desistir quando o contexto do cliente já foi cancelado: a
// conexão sendo fechada é exatamente o caso que esta chamada existe para
// informar, e pulá-la deixaria o usuário marcado como online para sempre.
// Por isso o envio bloqueia até o laço do hub recebê-lo (o que sempre
// acontece, já que o laço está rodando) ou até o próprio hub parar.
func (h *Hub) Unregister(client *Client) {
	select {
	case h.unregister <- client:
	case <-h.done:
		// O hub está desligando e vai fechar todas as conexões ele mesmo,
		// então não resta nada a informar.
	}
}

// Notify envia um evento externo ao laço do hub.
func (h *Hub) Notify(ctx context.Context, n Notification) {
	select {
	case h.notify <- n:
	case <-ctx.Done():
	}
}

// IsOnline informa se um usuário tem ao menos uma conexão. É chamado pelos
// handlers HTTP, daí a trava de leitura.
func (h *Hub) IsOnline(userID int64) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return len(h.clients[userID]) > 0
}

// OnlineUsers devolve o ID de todo usuário conectado.
func (h *Hub) OnlineUsers() []int64 {
	h.mu.RLock()
	defer h.mu.RUnlock()

	users := make([]int64, 0, len(h.clients))
	for userID := range h.clients {
		users = append(users, userID)
	}
	return users
}

// submit enfileira no laço do hub um frame recebido de um cliente.
func (h *Hub) submit(ctx context.Context, client *Client, env ws.Envelope, rawSize int) error {
	select {
	case h.incoming <- inbound{client: client, env: env, rawSize: rawSize}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// handleRegister registra a conexão e anuncia a presença quando ela é a
// primeira do usuário.
func (h *Hub) handleRegister(client *Client) {
	userID := client.UserID()

	// A trava de escrita é necessária mesmo rodando na goroutine do hub:
	// IsOnline e OnlineUsers leem o mesmo mapa a partir dos handlers HTTP,
	// e o detector de corrida acusa a escrita sem sincronização.
	h.mu.Lock()
	if h.clients[userID] == nil {
		h.clients[userID] = make(map[*Client]struct{})
	}
	alreadyOnline := len(h.clients[userID]) > 0
	h.clients[userID][client] = struct{}{}
	h.mu.Unlock()

	if alreadyOnline {
		return
	}

	slog.Info("user online", "user_id", userID)

	// Uma segunda conexão não é um novo evento de presença, então só a
	// primeira é anunciada.
	go h.broadcastPresence(userID, true)
}

// handleUnregister remove a conexão e anuncia a presença quando ela era a
// última do usuário.
func (h *Hub) handleUnregister(client *Client) {
	userID := client.UserID()

	h.mu.Lock()
	set, ok := h.clients[userID]
	if !ok {
		h.mu.Unlock()
		return
	}
	if _, present := set[client]; !present {
		h.mu.Unlock()
		return
	}
	delete(set, client)

	lastConnection := len(set) == 0
	if lastConnection {
		delete(h.clients, userID)
	}
	h.mu.Unlock()

	client.close()

	if !lastConnection {
		return
	}

	slog.Info("user offline", "user_id", userID)

	go h.broadcastPresence(userID, false)
}

// broadcastPresence avisa os contatos de um usuário que ele entrou ou
// saiu. Roda fora do laço do hub porque consulta o banco.
func (h *Hub) broadcastPresence(userID int64, online bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	contacts, err := h.store.ListContacts(ctx, userID)
	if err != nil {
		slog.Error("presence: list contacts failed", "user_id", userID, "error", err)
		return
	}

	payload := ws.PresencePayload{UserID: userID, Online: online}
	frame, err := encodeFrame(mustEnvelope(ws.TypePresence, payload))
	if err != nil {
		slog.Error("presence: encode failed", "error", err)
		return
	}

	for _, contactID := range contacts {
		h.deliverToUser(contactID, frame)
	}
}

// handleNotification reage a mudanças feitas pela API REST.
func (h *Hub) handleNotification(n Notification) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	switch n.Type {
	case NotifyConversationUpdated:
		// O cache de membros precisa ser descartado para que a próxima
		// mensagem use a participação atual em vez de uma foto antiga.
		delete(h.members, n.ConversationID)
		h.broadcastConversationUpdated(ctx, n.ConversationID, n.UserIDs)
	}
}

// broadcastConversationUpdated recarrega uma conversa e a envia aos
// membros, mais os destinatários extras, para que o cliente deles a
// atualize.
//
// Um destinatário extra é um usuário que não é mais membro: a atualização
// que ele recebe lista os membros sem ele, e é assim que o cliente dele
// descobre que foi removido.
func (h *Hub) broadcastConversationUpdated(ctx context.Context, conversationID int64, extra []int64) {
	conv, err := h.store.GetConversation(ctx, conversationID)
	if err != nil {
		slog.Error("conversation update: load failed", "conversation_id", conversationID, "error", err)
		return
	}

	members, err := h.store.ListMembers(ctx, conversationID)
	if err != nil {
		slog.Error("conversation update: list members failed", "conversation_id", conversationID, "error", err)
		return
	}
	conv.Members = members

	frame, err := encodeFrame(mustEnvelope(ws.TypeConversationUpd,
		ws.ConversationUpdatedPayload{Conversation: conv}))
	if err != nil {
		slog.Error("conversation update: encode failed", "error", err)
		return
	}

	recipients := make(map[int64]struct{}, len(members)+len(extra))
	for _, member := range members {
		recipients[member.UserID] = struct{}{}
	}
	for _, userID := range extra {
		recipients[userID] = struct{}{}
	}
	for userID := range recipients {
		h.deliverToUser(userID, frame)
	}
}

// mustEnvelope monta um envelope e só entra em panic por erro de
// programação: estes payloads são structs simples que não falham ao
// serializar.
func mustEnvelope(msgType string, payload any) ws.Envelope {
	env, err := ws.NewEnvelope(msgType, payload)
	if err != nil {
		panic(err)
	}
	return env
}

// deliverToUser enfileira um frame em todas as conexões de um usuário.
//
// Usa a trava de leitura porque é chamado tanto pelo laço do hub quanto
// por goroutines de fundo (anúncios de presença).
func (h *Hub) deliverToUser(userID int64, frame []byte) {
	h.mu.RLock()
	targets := make([]*Client, 0, len(h.clients[userID]))
	for c := range h.clients[userID] {
		targets = append(targets, c)
	}
	h.mu.RUnlock()

	for _, c := range targets {
		if c.trySend(frame) {
			continue
		}

		// A fila está cheia: este cliente está lento demais. Desconectá-lo
		// protege os outros membros de esperar por ele.
		slog.Warn("dropping slow client", "user_id", c.UserID())

		// A remoção precisa passar pelo laço do hub, não ser feita aqui:
		// esta função roda na goroutine do laço para alguns chamadores e
		// numa goroutine de fundo para outros, então mexer no mapa
		// diretamente geraria corrida. Fechar primeiro desbloqueia a
		// goroutine de escrita, que então dispara o caminho normal de
		// unregister.
		c.closeWith(websocket.StatusPolicyViolation, closeSlowReason)
		h.dropClient(c)
	}
}

// dropClient enfileira um cliente que o próprio hub decidiu desconectar.
//
// A remoção tem de acontecer na goroutine do laço, porque deliverToUser
// também é chamado por goroutines de fundo (anúncios de presença), onde
// mexer no mapa de participação geraria corrida. O envio não bloqueia, num
// canal dedicado com buffer, para nunca travar o laço que o chama; uma
// fila cheia descarta o pedido, o que é inofensivo porque a conexão já
// está fechada e a goroutine dona dela vai informar o unregister.
func (h *Hub) dropClient(c *Client) {
	select {
	case h.drop <- c:
	default:
	}
}

// conversationMembers devolve os IDs dos membros de uma conversa,
// carregando-os do banco uma vez e guardando o resultado em cache.
//
// Run é o único chamador, então o cache não precisa de trava própria.
func (h *Hub) conversationMembers(ctx context.Context, conversationID int64) (map[int64]struct{}, error) {
	if cached, ok := h.members[conversationID]; ok {
		return cached, nil
	}

	members, err := h.store.ListMembers(ctx, conversationID)
	if err != nil {
		return nil, err
	}

	set := make(map[int64]struct{}, len(members))
	for _, m := range members {
		set[m.UserID] = struct{}{}
	}
	h.members[conversationID] = set

	return set, nil
}

// rememberAck guarda um ACK para deduplicação.
//
// Quando o cache enche, ele é esvaziado em vez de descartar entradas uma a
// uma: é um atalho de melhor esforço, e o índice UNIQUE (sender_id,
// client_id) do banco continua garantindo a correção.
func (h *Hub) rememberAck(key ackKey, ack ws.AckPayload) {
	if len(h.acks) >= maxAckCacheEntries {
		h.acks = make(map[ackKey]ws.AckPayload)
	}
	h.acks[key] = ack
}

// memberIDs extrai os IDs dos membros de um conjunto de participação numa
// ordem estável, para que a distribuição não dependa da ordem de
// iteração do mapa.
func memberIDs(members map[int64]struct{}) []int64 {
	ids := make([]int64, 0, len(members))
	for id := range members {
		ids = append(ids, id)
	}
	return ids
}

// NewClient monta o objeto de uma conexão para o handler WebSocket.
//
// O handler é dono dos laços de leitura e escrita; o hub só conhece o
// cliente pelo registro, pela fila de saída e por este construtor, o que
// impede os dois pacotes de dependerem dos detalhes internos um do outro.
func (h *Hub) NewClient(userID int64, conn *websocket.Conn) *Client {
	client, _ := newClient(userID, conn, h.cfg.ClientQueueSize)
	return client
}

// Submit enfileira no laço do hub um frame vindo de um cliente.
func (h *Hub) Submit(ctx context.Context, client *Client, env ws.Envelope, rawSize int) error {
	return h.submit(ctx, client, env, rawSize)
}
