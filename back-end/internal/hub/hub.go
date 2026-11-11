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
	// Enqueue accepts a message for asynchronous writing and returns a
	// channel that yields the stored row, with its database-assigned ID,
	// once the batch has been committed. The channel always receives
	// exactly one value: a zero Message when the row was not stored.
	Enqueue(ctx context.Context, msg models.Message) (<-chan models.Message, error)
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

	// clients is guarded by mu because HTTP handlers and background
	// goroutines read it. The maps below it are owned by the Run loop
	// alone and must never be touched from another goroutine.
	mu      sync.RWMutex
	clients map[int64]map[*Client]struct{}
	members map[int64]map[int64]struct{} // conversation_id -> set of user_id
	acks    map[ackKey]ws.AckPayload     // (sender_id, client_id) -> previous ack
	seqs    map[int64]int64              // conversation_id -> last assigned sequence

	// unsaved holds the messages handed to the persistence pool that the
	// database has not confirmed yet (conversation_id -> sequence ->
	// message). A sync merges it with the database query, so a message
	// accepted moments before a reconnect is not missing from the backfill.
	unsaved map[int64]map[int64]models.Message

	// Channels into the Run loop. Only Run reads them.
	register   chan *Client
	unregister chan *Client

	// persisted reports that the pool finished with a message (stored or
	// not). It lets the loop enrich the dedup cache and drop the message
	// from unsaved without the waiting goroutine touching hub state.
	persisted chan persistedMsg

	// receipts feeds the goroutine that writes delivered/read watermarks
	// in batches. Only the loop sends on it, and shutdown closes it.
	receipts chan db.Receipt

	// drop is a buffered queue of connections the hub itself decided to
	// disconnect (slow clients). It is separate from unregister so the
	// loop can enqueue without ever blocking on itself, and buffered so
	// a burst of drops never stalls delivery.
	drop     chan *Client
	incoming chan inbound
	notify   chan Notification

	// done is closed when Run returns, so callers that need to hand work
	// to the loop can tell that it is gone instead of blocking forever.
	done chan struct{}

	// wg tracks the long-lived goroutines so shutdown can wait for them.
	wg sync.WaitGroup
}

// persistedMsg tells the loop the pool has answered for one message.
// stored is false when the row was not written (a duplicate or a failed
// write); ack then carries no database ID.
type persistedMsg struct {
	key            ackKey
	ack            ws.AckPayload
	stored         bool
	conversationID int64
	sequence       int64
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
)

// Notification is an event raised outside the WebSocket path.
type Notification struct {
	Type           NotificationType
	ConversationID int64
	// UserIDs are extra recipients who are no longer members, such as a
	// user who was just removed: they still need the update to learn
	// that the conversation is gone for them.
	UserIDs []int64
}

// shutdownGrace is how long the hub waits for the shutdown notice to
// reach the clients before closing their connections.
const shutdownGrace = 100 * time.Millisecond

// maxAckCacheEntries bounds the deduplication cache. A client that
// reconnects and resends will do so quickly, so only recent entries
// matter; the database unique index remains the real guarantee.
const maxAckCacheEntries = 4096

// Receipt batching: the writer folds confirmations for up to
// receiptFlushEvery, or until maxReceiptBatch members are pending, into
// one transaction.
const (
	receiptFlushEvery = 100 * time.Millisecond
	maxReceiptBatch   = 256
	receiptQueueSize  = 4096
)

// Close codes and reasons sent when the hub itself ends a connection.
const (
	closeSlowReason     = "client too slow"
	closeShutdownReason = "server shutting down"
)

// New builds a hub. The persister may be nil, in which case messages are
// delivered but not stored (useful in tests).
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

// Run is the hub's event loop. It blocks until ctx is cancelled.
//
// No handler ever touches the maps directly: everything arrives as a
// message on one of the channels below, which serialises all mutation
// through this single select.
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

	// Tell the clients the server is going away before their queues are
	// closed, so the frame is actually written. Without this the client
	// would just see the socket drop and would have to guess whether to
	// reconnect.
	if frame, err := encodeFrame(ws.Envelope{Type: ws.TypeServerShutdown}); err == nil {
		for _, c := range clients {
			c.trySend(frame)
		}

		// Give the write goroutines a moment to flush. This is a fixed
		// grace period rather than a wait group because a client may be
		// slow or already gone, and shutdown must not hang on it.
		time.Sleep(shutdownGrace)
	}

	for _, c := range clients {
		c.closeWith(websocket.StatusGoingAway, closeShutdownReason)
	}

	// The loop is the only sender on receipts, and it has stopped, so the
	// channel can be closed: the writer flushes what it holds and returns.
	close(h.receipts)

	h.wg.Wait()
	slog.Info("hub stopped", "clients_notified", len(clients))
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
		h.broadcastConversationUpdated(ctx, n.ConversationID, n.UserIDs)
	}
}

// broadcastConversationUpdated reloads a conversation and sends it to its
// members, plus any extra recipients, so their client can refresh it.
//
// An extra recipient is a user who is no longer a member: the update they
// receive lists the members without them, which is how their client learns
// it was removed.
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
		if c.trySend(frame) {
			continue
		}

		// The queue is full: this client is too slow. Disconnecting it
		// protects the other members from waiting on it.
		slog.Warn("dropping slow client", "user_id", c.UserID())

		// The removal must go through the hub loop, not be done here:
		// this function runs on the loop goroutine for some callers and
		// on a background goroutine for others, so touching the map
		// directly would race. Closing first unblocks the write
		// goroutine, which then triggers the normal unregister path.
		c.closeWith(websocket.StatusPolicyViolation, closeSlowReason)
		h.dropClient(c)
	}
}

// dropClient queues a client that the hub itself decided to disconnect.
//
// The removal has to happen on the loop goroutine, because deliverToUser
// is also called from background goroutines (presence broadcasts) where
// touching the membership map would race. The send is non-blocking on a
// dedicated buffered channel so it can never stall the loop that is
// calling it; a full queue drops the request, which is harmless because
// the connection is already closed and whichever goroutine owns it will
// report the unregister.
func (h *Hub) dropClient(c *Client) {
	select {
	case h.drop <- c:
	default:
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
