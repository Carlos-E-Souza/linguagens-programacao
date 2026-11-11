package hub

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/db"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/ws"
)

// handleIncoming dispatches one frame from one client.
//
// This runs on the hub goroutine, so it must not block: the only
// operations allowed here are map lookups, sequence assignment and
// channel sends. Anything touching the database or the network happens
// in a separate goroutine.
func (h *Hub) handleIncoming(ctx context.Context, in inbound) {
	switch in.env.Type {
	case ws.TypeMessageSend:
		h.handleMessageSend(ctx, in)
	case ws.TypeMessageDelivered:
		h.handleReceipt(ctx, in, models.StatusDelivered)
	case ws.TypeMessageRead:
		h.handleReceipt(ctx, in, models.StatusRead)
	case ws.TypeSync:
		h.handleSync(ctx, in)
	default:
		h.sendError(in.client, "unknown_type", "unsupported message type", "")
	}
}

// handleMessageSend validates, assigns a sequence, acknowledges and fans
// the message out to the members of the conversation.
func (h *Hub) handleMessageSend(ctx context.Context, in inbound) {
	var payload ws.SendPayload
	if err := json.Unmarshal(in.env.Data, &payload); err != nil {
		h.sendError(in.client, "invalid_payload", "malformed message payload", "")
		return
	}

	senderID := in.client.UserID()
	payload.ClientID = strings.TrimSpace(payload.ClientID)
	payload.Body = strings.TrimSpace(payload.Body)

	if payload.ClientID == "" {
		h.sendError(in.client, "missing_client_id", "client_id is required", "")
		return
	}
	if payload.ConversationID <= 0 {
		h.sendError(in.client, "invalid_conversation_id", "conversation_id is required", payload.ClientID)
		return
	}
	if payload.Body == "" {
		h.sendError(in.client, "empty_body", "message body must not be empty", payload.ClientID)
		return
	}
	if len([]rune(payload.Body)) > h.cfg.MessageLimitChars {
		h.sendError(in.client, "body_too_long",
			"message body exceeds the maximum length", payload.ClientID)
		return
	}

	// Deduplication: a client that did not receive its acknowledgement
	// resends the same client_id, and must get the same answer instead of
	// a second copy of the message.
	key := ackKey{senderID: senderID, clientID: payload.ClientID}
	if previous, ok := h.acks[key]; ok {
		frame, err := encodeFrame(mustEnvelope(ws.TypeMessageAck, previous))
		if err == nil {
			in.client.trySend(frame)
		}
		return
	}

	members, err := h.conversationMembers(ctx, payload.ConversationID)
	if err != nil {
		h.sendError(in.client, "conversation_not_found", "conversation not found", payload.ClientID)
		return
	}

	if _, isMember := members[senderID]; !isMember {
		// Answering as if the conversation were missing avoids revealing
		// that it exists to somebody outside it.
		h.sendError(in.client, "not_a_member", "you are not a member of this conversation", payload.ClientID)
		return
	}

	// Run is the only writer, so incrementing the counter here cannot
	// race. On the first message of a conversation the value is primed
	// from the database.
	sequence, err := h.nextSequence(ctx, payload.ConversationID)
	if err != nil {
		h.sendError(in.client, "internal_error", "could not assign a sequence", payload.ClientID)
		return
	}

	now := time.Now().UTC()
	msg := models.Message{
		ConversationID: payload.ConversationID,
		Sequence:       sequence,
		SenderID:       senderID,
		ClientID:       payload.ClientID,
		Body:           payload.Body,
		CreatedAt:      now,
	}

	ack := ws.AckPayload{
		ClientID:       payload.ClientID,
		Sequence:       sequence,
		ConversationID: payload.ConversationID,
		CreatedAt:      now.Format(time.RFC3339Nano),
	}

	// The real message ID is assigned by the database, so the message goes
	// to the persistence pool and a goroutine waits for the stored row.
	// Until the pool answers, the message is kept in unsaved so that a
	// sync arriving in that window still finds it.
	var result <-chan models.Message
	if h.persister != nil {
		var err error
		result, err = h.persister.Enqueue(ctx, msg)
		if err != nil {
			// The message was not accepted, so nothing about it may remain:
			// a cached ACK would answer the client's retry as if it had been
			// stored, and a consumed sequence would leave a permanent hole
			// that clients filling gaps would never see closed. Run is the
			// only writer and nothing ran since nextSequence, so giving the
			// number back is safe.
			slog.Error("enqueue failed", "conversation_id", msg.ConversationID, "error", err)
			h.seqs[payload.ConversationID] = sequence - 1
			h.sendError(in.client, "internal_error", "could not persist the message", payload.ClientID)
			return
		}
		h.rememberUnsaved(msg)
	}

	// Only now the message is really accepted: a resend of the same
	// client_id gets this answer instead of a second copy.
	h.rememberAck(key, ack)

	// Deliver to every member, including the sender's other connections,
	// so a second window of the same user stays in sync.
	newFrame, err := encodeFrame(mustEnvelope(ws.TypeMessageNew, msg))
	if err != nil {
		slog.Error("encode message.new failed", "error", err)
		return
	}
	for userID := range members {
		h.deliverToUser(userID, newFrame)
	}

	if result == nil {
		// No persistence (tests): answer right away, without an ID.
		h.sendAck(in.client, ack)
		return
	}

	// One goroutine per message owns the acknowledgement, so the hub loop
	// never waits on disk. It must not touch hub state; everything it
	// learns goes back to the loop through h.persisted.
	go h.awaitPersisted(in.client, key, ack, msg, result)
}

// awaitPersisted sends the acknowledgement once the pool has stored the
// message, or after ackWaitTimeout without an ID, and then reports the
// outcome to the loop.
//
// It always waits for the pool's answer, even after a timed-out ACK went
// out, because the loop must learn when the message left the queue to
// drop it from unsaved. The pool answers every message, also on failure
// and during shutdown, so the wait ends.
func (h *Hub) awaitPersisted(client *Client, key ackKey, ack ws.AckPayload, msg models.Message, result <-chan models.Message) {
	timer := time.NewTimer(ackWaitTimeout)
	defer timer.Stop()

	var row models.Message
	acked := false

	select {
	case row = <-result:
	case <-timer.C:
		slog.Warn("ack sent before the message was stored",
			"conversation_id", msg.ConversationID, "client_id", key.clientID)
		h.sendAck(client, ack)
		acked = true
		row = <-result
	}

	final := ack
	stored := row.ID != 0
	if stored {
		final.ID = row.ID
		final.CreatedAt = row.CreatedAt.Format(time.RFC3339Nano)
	}

	if !acked {
		h.sendAck(client, final)
	}

	select {
	case h.persisted <- persistedMsg{
		key:            key,
		ack:            final,
		stored:         stored,
		conversationID: msg.ConversationID,
		sequence:       msg.Sequence,
	}:
	case <-h.done:
		// The hub stopped; there is no state left to update.
	}
}

// sendAck queues a message.ack frame for one connection.
func (h *Hub) sendAck(client *Client, ack ws.AckPayload) {
	frame, err := encodeFrame(mustEnvelope(ws.TypeMessageAck, ack))
	if err != nil {
		slog.Error("encode message.ack failed", "error", err)
		return
	}
	client.trySend(frame)
}

// rememberUnsaved records a message the database has not confirmed yet.
// Run is the only caller.
func (h *Hub) rememberUnsaved(msg models.Message) {
	byConv := h.unsaved[msg.ConversationID]
	if byConv == nil {
		byConv = make(map[int64]models.Message)
		h.unsaved[msg.ConversationID] = byConv
	}
	byConv[msg.Sequence] = msg
}

// handlePersisted runs on the loop when the pool has answered for one
// message: the message leaves unsaved, and the dedup cache learns the
// database ID when there is one.
func (h *Hub) handlePersisted(pm persistedMsg) {
	if byConv := h.unsaved[pm.conversationID]; byConv != nil {
		delete(byConv, pm.sequence)
		if len(byConv) == 0 {
			delete(h.unsaved, pm.conversationID)
		}
	}

	if pm.stored {
		h.rememberAck(pm.key, pm.ack)
	}
}

// ackWaitTimeout bounds how long the acknowledgement waits for the
// database-assigned ID.
//
// The persistence pool flushes on a 200ms ticker, so this comfortably
// covers a normal write while still guaranteeing a reply if the database
// is slow or unavailable: the message is delivered regardless, and the
// client learns the ID from message.new instead.
const ackWaitTimeout = 2 * time.Second

// nextSequence returns the next sequence number for a conversation,
// priming the in-memory counter from the database on first use.
func (h *Hub) nextSequence(ctx context.Context, conversationID int64) (int64, error) {
	current, ok := h.seqs[conversationID]
	if !ok {
		if h.store == nil {
			current = 0
		} else {
			stored, err := h.store.LastSequence(ctx, conversationID)
			if err != nil {
				return 0, err
			}
			current = stored
		}
	}

	next := current + 1
	h.seqs[conversationID] = next

	return next, nil
}

// handleReceipt processes a delivered or read confirmation.
//
// The status goes out to the other members right away. The watermark write
// is handed to the receipt writer, which batches it with the other
// confirmations of the next few milliseconds.
func (h *Hub) handleReceipt(ctx context.Context, in inbound, status string) {
	var payload ws.ReadReceiptPayload
	if err := json.Unmarshal(in.env.Data, &payload); err != nil {
		h.sendError(in.client, "invalid_payload", "malformed receipt payload", "")
		return
	}
	if payload.UpToSequence <= 0 {
		h.sendError(in.client, "invalid_sequence", "up_to_sequence must be a positive integer", "")
		return
	}

	userID := in.client.UserID()

	members, err := h.conversationMembers(ctx, payload.ConversationID)
	if err != nil {
		h.sendError(in.client, "conversation_not_found", "conversation not found", "")
		return
	}
	if _, isMember := members[userID]; !isMember {
		h.sendError(in.client, "not_a_member", "you are not a member of this conversation", "")
		return
	}

	h.queueReceipt(payload.ConversationID, userID, payload.UpToSequence, status)

	statusFrame, err := encodeFrame(mustEnvelope(ws.TypeMessageStatus, ws.StatusPayload{
		ConversationID: payload.ConversationID,
		UserID:         userID,
		UpToSequence:   payload.UpToSequence,
		Status:         status,
	}))
	if err != nil {
		slog.Error("encode message.status failed", "error", err)
		return
	}

	for memberID := range members {
		if memberID == userID {
			continue
		}
		h.deliverToUser(memberID, statusFrame)
	}
}

// queueReceipt hands a confirmation to the receipt writer without
// blocking the loop.
//
// When the queue is full the write is skipped and logged. That loses
// nothing for good: watermarks are cumulative, so the member's next
// confirmation carries this one with it.
func (h *Hub) queueReceipt(conversationID, userID, upTo int64, status string) {
	if h.store == nil {
		return
	}

	r := db.Receipt{ConversationID: conversationID, UserID: userID}
	if status == models.StatusRead {
		r.Read = upTo
	} else {
		r.Delivered = upTo
	}

	select {
	case h.receipts <- r:
	default:
		slog.Warn("receipt queue full, skipping write",
			"conversation_id", conversationID, "user_id", userID, "status", status)
	}
}

// runReceiptWriter folds confirmations into one entry per (conversation,
// member), keeping the highest delivered and read sequences, and writes
// them in one transaction every receiptFlushEvery or when maxReceiptBatch
// members are pending. It returns after the queue is closed and flushed.
func (h *Hub) runReceiptWriter() {
	defer h.wg.Done()

	type memberKey struct{ conversationID, userID int64 }
	pending := make(map[memberKey]db.Receipt)

	flush := func() {
		if len(pending) == 0 {
			return
		}

		batch := make([]db.Receipt, 0, len(pending))
		for _, r := range pending {
			batch = append(batch, r)
		}
		clear(pending)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := h.store.ApplyReceipts(ctx, batch); err != nil {
			slog.Error("persist receipts failed", "count", len(batch), "error", err)
		}
	}

	ticker := time.NewTicker(receiptFlushEvery)
	defer ticker.Stop()

	for {
		select {
		case r, ok := <-h.receipts:
			if !ok {
				flush()
				return
			}

			key := memberKey{r.ConversationID, r.UserID}
			current := pending[key]
			current.ConversationID, current.UserID = r.ConversationID, r.UserID
			current.Delivered = max(current.Delivered, r.Delivered)
			current.Read = max(current.Read, r.Read)
			pending[key] = current

			if len(pending) >= maxReceiptBatch {
				flush()
			}

		case <-ticker.C:
			flush()
		}
	}
}

// maxSyncMessagesPerConversation bounds the offline backfill. A client
// that has been away long enough to exceed this uses the REST pagination
// instead, which keeps one frame from carrying an unbounded payload.
const maxSyncMessagesPerConversation = 500

// handleSync sends the messages a reconnecting client missed.
func (h *Hub) handleSync(ctx context.Context, in inbound) {
	var payload ws.SyncPayload
	if err := json.Unmarshal(in.env.Data, &payload); err != nil {
		h.sendError(in.client, "invalid_payload", "malformed sync payload", "")
		return
	}

	userID := in.client.UserID()
	client := in.client

	// Snapshot the messages the database has not confirmed yet. Taken here,
	// on the loop, before the query below: a message that is written
	// after the snapshot is in it, and one written before is in the
	// database, so the merge cannot miss either. Messages accepted after
	// the snapshot reach this connection live, since it is registered.
	unsaved := make(map[int64][]models.Message, len(h.unsaved))
	for convID, byConv := range h.unsaved {
		for _, msg := range byConv {
			unsaved[convID] = append(unsaved[convID], msg)
		}
	}

	// Building the backfill reads the database, so it runs off the hub
	// loop; the frames are queued through the same buffered channel.
	go func() {
		syncCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if h.store == nil {
			return
		}

		// Start from what the client reported, then also cover
		// conversations it did not mention (a group created while it was
		// offline, for instance).
		known := make(map[int64]int64, len(payload.Conversations))
		for _, c := range payload.Conversations {
			known[c.ConversationID] = c.LastSequence
		}

		conversations, err := h.store.ListUserConversations(syncCtx, userID)
		if err != nil {
			slog.Error("sync: list conversations failed", "user_id", userID, "error", err)
			return
		}

		for _, conv := range conversations {
			from := known[conv.ID]

			stored, err := h.store.ListMessagesAfter(syncCtx, conv.ID, from, maxSyncMessagesPerConversation)
			if err != nil {
				slog.Error("sync: list messages failed", "conversation_id", conv.ID, "error", err)
				continue
			}

			messages := mergeBackfill(stored, unsaved[conv.ID], from, maxSyncMessagesPerConversation)

			for _, msg := range messages {
				frame, err := encodeFrame(mustEnvelope(ws.TypeMessageNew, msg))
				if err != nil {
					continue
				}
				if !client.trySend(frame) {
					// The queue filled up: the client is too slow to
					// receive the whole backfill. Stop sending, but fall
					// through so sync.end still goes out below — without
					// it the client would wait forever, not knowing the
					// backfill had finished.
					slog.Warn("sync: client queue full, truncating backfill",
						"user_id", userID, "conversation_id", conv.ID)
					break
				}
			}
		}

		endFrame, err := encodeFrame(ws.Envelope{Type: ws.TypeSyncEnd})
		if err != nil {
			slog.Error("sync: encode sync.end failed", "error", err)
			return
		}
		if !client.trySend(endFrame) {
			slog.Warn("sync: could not queue sync.end", "user_id", userID)
		}
	}()
}

// mergeBackfill combines the stored messages after a sequence with the
// unconfirmed ones, dropping duplicates (a message can be in both when it
// was written between the snapshot and the query), in ascending sequence
// order and capped at limit.
func mergeBackfill(stored, unsaved []models.Message, after int64, limit int) []models.Message {
	if len(unsaved) == 0 {
		return stored
	}

	bySequence := make(map[int64]models.Message, len(stored)+len(unsaved))
	for _, msg := range unsaved {
		if msg.Sequence > after {
			bySequence[msg.Sequence] = msg
		}
	}
	// Stored rows win: they carry the database ID.
	for _, msg := range stored {
		bySequence[msg.Sequence] = msg
	}

	merged := make([]models.Message, 0, len(bySequence))
	for _, msg := range bySequence {
		merged = append(merged, msg)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Sequence < merged[j].Sequence })

	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

// sendError reports a rejected request to the client that made it.
func (h *Hub) sendError(client *Client, code, message, clientID string) {
	frame, err := encodeFrame(mustEnvelope(ws.TypeError, ws.ErrorPayload{
		Code:     code,
		Message:  message,
		ClientID: clientID,
	}))
	if err != nil {
		slog.Error("encode error frame failed", "error", err)
		return
	}
	client.trySend(frame)
}
