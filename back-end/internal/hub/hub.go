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

// Persister accepts a message for asynchronous writing.
//
// The hub depends on this narrow interface rather than on store.Pool, so
// the two are independently testable and the hub cannot accidentally
// block on a database call.
type Persister interface {
	Enqueue(ctx context.Context, msg models.Message) error
}

// Hub owns every connected client and routes messages between them.
//
// Concurrency model, which is the heart of this package:
//
//   - A single goroutine (Run) mutates the hub's state. Because only that
//     goroutine writes, the membership maps need no lock, and sequence
//     numbers can be assigned without a race.
//   - Other goroutines (HTTP handlers, connection writers) only read.
//     Those reads are guarded by a sync.RWMutex, which allows many
//     concurrent readers.
//   - Every client has a buffered outgoing channel, so the hub never
//     performs a socket write and never blocks on a slow peer.
type Hub struct {
	cfg       config.Config
	store     *db.Store
	persister Persister

	// Guarded by mu; read from other goroutines.
	mu      sync.RWMutex
	clients map[int64]map[*Client]struct{}
	members map[int64]map[int64]struct{} // conversation_id -> set of user_id
	pending map[int64]map[int64]struct{} // conversation_id -> user_id not yet written to DB
	acks    map[ackKey]ws.AckPayload     // (sender_id, client_id) -> previous ack
	seqs    map[int64]int64              // conversation_id -> last assigned sequence

	// Channels into the Run loop. Only Run reads them.
	register   chan *Client
	unregister chan *Client
	incoming   chan inbound
	notify     chan Notification

	// done is closed when Run returns, so callers that need to hand work
	// to the loop can tell that it is gone instead of blocking forever.
	done chan struct{}

	// wg tracks the long-lived goroutines so shutdown can wait for them.
	wg sync.WaitGroup
}

// ackKey identifies a client-supplied message ID within a sender.
type ackKey struct {
	senderID int64
	clientID string
}

// inbound is a frame received from one client.
type inbound struct {
	client  *Client
	env     ws.Envelope
	rawSize int
}

// NotificationType distinguishes the events the REST layer pushes into
// the hub so that connected clients learn about changes made over HTTP.
type NotificationType string

const (
	// NotifyConversationUpdated tells the members of a conversation that
	// its membership changed.
	NotifyConversationUpdated NotificationType = "conversation_updated"
	// NotifyMembersAdded tells specific users they were added to a group.
	NotifyMembersAdded NotificationType = "members_added"
)

// Notification is an event raised outside the WebSocket path.
type Notification struct {
	Type           NotificationType
	ConversationID int64
	UserIDs        []int64
}

// maxAckCacheEntries bounds the deduplication cache. A client that
// reconnects and resends will do so quickly, so only recent entries
// matter; the database unique index remains the real guarantee.
const maxAckCacheEntries = 4096

// New builds a hub. The persister may be nil, in which case messages are
// delivered but not stored (useful in tests).
func New(cfg config.Config, store *db.Store, persister Persister) *Hub {
	return &Hub{
		cfg:        cfg,
		store:      store,
		persister:  persister,
		clients:    make(map[int64]map[*Client]struct{}),
		members:    make(map[int64]map[int64]struct{}),
		pending:    make(map[int64]map[int64]struct{}),
		acks:       make(map[ackKey]ws.AckPayload),
		seqs:       make(map[int64]int64),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		incoming:   make(chan inbound, 256),
		notify:     make(chan Notification, 128),
		done:       make(chan struct{}),
	}
}

// Run is the hub's event loop. It blocks until ctx is cancelled.
//
// No handler ever touches the maps directly: everything arrives as a
// message on one of the channels below, which serialises all mutation
// through this single select.
func (h *Hub) Run(ctx context.Context) {
	slog.Info("hub started")

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

		case in := <-h.incoming:
			h.handleIncoming(ctx, in)

		case n := <-h.notify:
			h.handleNotification(n)
		}
	}
}

// shutdown closes every connection and waits for the goroutines.
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

	for _, c := range clients {
		c.close()
	}

	h.wg.Wait()
	slog.Info("hub stopped")
}

// Register adds a connection to the hub.
func (h *Hub) Register(client *Client) {
	select {
	case h.register <- client:
	case <-client.context().Done():
	}
}

// Unregister removes a connection from the hub.
//
// It must not give up when the client context is already cancelled: the
// closing connection is exactly the case this call exists to report, and
// skipping it would leave the user marked online forever. The send
// therefore blocks until the hub loop picks it up (it always does, since
// the loop is running) or the hub itself stops.
func (h *Hub) Unregister(client *Client) {
	select {
	case h.unregister <- client:
	case <-h.done:
		// The hub is shutting down and will close every connection
		// itself, so there is nothing left to report.
	}
}

// Notify pushes an external event into the hub loop.
func (h *Hub) Notify(ctx context.Context, n Notification) {
	select {
	case h.notify <- n:
	case <-ctx.Done():
	}
}

// IsOnline reports whether a user has at least one connection. It is
// called from HTTP handlers, hence the read lock.
func (h *Hub) IsOnline(userID int64) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return len(h.clients[userID]) > 0
}

// OnlineUsers returns every connected user ID.
func (h *Hub) OnlineUsers() []int64 {
	h.mu.RLock()
	defer h.mu.RUnlock()

	users := make([]int64, 0, len(h.clients))
	for userID := range h.clients {
		users = append(users, userID)
	}
	return users
}

// submit queues a frame received from a client into the hub loop.
func (h *Hub) submit(ctx context.Context, client *Client, env ws.Envelope, rawSize int) error {
	select {
	case h.incoming <- inbound{client: client, env: env, rawSize: rawSize}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// handleRegister records the connection and announces presence when it is
// the user's first one.
func (h *Hub) handleRegister(client *Client) {
	userID := client.UserID()

	// The write lock is required even though this runs on the hub
	// goroutine: IsOnline and OnlineUsers read the same map from HTTP
	// handlers, and the race detector flags the unsynchronised write.
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

	// A second connection is not a new presence event, so only the first
	// one is broadcast.
	go h.broadcastPresence(userID, true)
}

// handleUnregister drops the connection and announces presence when it
// was the user's last one.
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

// broadcastPresence tells a user's contacts that they came online or went
// offline. It runs off the hub loop because it touches the database.
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

// handleNotification reacts to changes made through the REST API.
func (h *Hub) handleNotification(n Notification) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	switch n.Type {
	case NotifyConversationUpdated:
		// The member cache must be dropped so the next message uses the
		// current membership rather than a stale snapshot.
		delete(h.members, n.ConversationID)
		h.broadcastConversationUpdated(ctx, n.ConversationID)

	case NotifyMembersAdded:
		delete(h.members, n.ConversationID)
	}
}

// broadcastConversationUpdated reloads a conversation and sends it to its
// members so their client can refresh the member list.
func (h *Hub) broadcastConversationUpdated(ctx context.Context, conversationID int64) {
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

	for _, member := range members {
		h.deliverToUser(member.UserID, frame)
	}
}

// mustEnvelope builds an envelope, panicking only on a programming error:
// these payloads are plain structs that cannot fail to marshal.
func mustEnvelope(msgType string, payload any) ws.Envelope {
	env, err := ws.NewEnvelope(msgType, payload)
	if err != nil {
		panic(err)
	}
	return env
}

// deliverToUser queues a frame to every connection of one user.
//
// It takes the read lock because it is called both from the hub loop and
// from background goroutines (presence broadcasts).
func (h *Hub) deliverToUser(userID int64, frame []byte) {
	h.mu.RLock()
	targets := make([]*Client, 0, len(h.clients[userID]))
	for c := range h.clients[userID] {
		targets = append(targets, c)
	}
	h.mu.RUnlock()

	for _, c := range targets {
		if !c.trySend(frame) {
			// The queue is full: this client is too slow. Disconnecting
			// it protects the other members from waiting on it.
			slog.Warn("dropping slow client", "user_id", c.UserID())
			c.close()
		}
	}
}

// conversationMembers returns the member IDs of a conversation, loading
// them from the database once and caching the result.
//
// Run is the only caller, so the cache needs no lock of its own.
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

// rememberAck stores an acknowledgement for deduplication.
//
// When the cache is full it is emptied rather than evicted one by one:
// this is a best-effort fast path, and the UNIQUE (sender_id, client_id)
// index in the database still guarantees correctness.
func (h *Hub) rememberAck(key ackKey, ack ws.AckPayload) {
	if len(h.acks) >= maxAckCacheEntries {
		h.acks = make(map[ackKey]ws.AckPayload)
	}
	h.acks[key] = ack
}

// memberIDs extracts the member IDs from a membership set in a stable
// order, so a fan-out does not depend on map iteration order.
func memberIDs(members map[int64]struct{}) []int64 {
	ids := make([]int64, 0, len(members))
	for id := range members {
		ids = append(ids, id)
	}
	return ids
}

// NewClient builds a connection handle for the WebSocket handler.
//
// The handler owns the read and write loops; the hub only knows the
// client through the registration, the outgoing queue and this
// constructor, which keeps the two packages from depending on each
// other's internals.
func (h *Hub) NewClient(userID int64, conn *websocket.Conn) *Client {
	client, _ := newClient(userID, conn, h.cfg.ClientQueueSize)
	return client
}

// Submit queues a frame from a client into the hub loop.
func (h *Hub) Submit(ctx context.Context, client *Client, env ws.Envelope, rawSize int) error {
	return h.submit(ctx, client, env, rawSize)
}
