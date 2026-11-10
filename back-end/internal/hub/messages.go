package hub

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

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
	case ws.TypeTyping:
		h.handleTyping(in)
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
	h.rememberAck(key, ack)

	// The real message ID is assigned by the database, so the
	// acknowledgement is finalised once the write succeeds.
	if h.persister != nil {
		if err := h.persister.Enqueue(ctx, msg); err != nil {
			slog.Error("enqueue failed", "conversation_id", msg.ConversationID, "error", err)
			h.sendError(in.client, "internal_error", "could not persist the message", payload.ClientID)
			return
		}
	}

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

	// The acknowledgement is sent immediately, without waiting for the
	// database. Blocking here would defeat the whole point of the async
	// persistence pool, and it would stall the hub loop on disk I/O.
	//
	// The consequence is that ack.ID is zero on the very first delivery:
	// the row has not been written yet, so no ID exists. The client
	// already learns the real ID from the message.new frame above, which
	// the persistence worker fills in, and a duplicate resend gets the
	// same zero-ID acknowledgement it got the first time, which is the
	// property that actually matters for deduplication.
	ackFrame, err := encodeFrame(mustEnvelope(ws.TypeMessageAck, ack))
	if err != nil {
		slog.Error("encode message.ack failed", "error", err)
		return
	}
	in.client.trySend(ackFrame)
}

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
func (h *Hub) handleReceipt(ctx context.Context, in inbound, status string) {
	var payload ws.ReadReceiptPayload
	if err := json.Unmarshal(in.env.Data, &payload); err != nil {
		h.sendError(in.client, "invalid_payload", "malformed receipt payload", "")
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

	// The watermark update is a database write, so it happens off the hub
	// loop and the broadcast follows it.
	go h.persistAndBroadcastReceipt(payload.ConversationID, userID, payload.UpToSequence, status)

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

// persistAndBroadcastReceipt writes the watermark and tells the sender.
func (h *Hub) persistAndBroadcastReceipt(conversationID, userID, upToSequence int64, status string) {
	if h.store == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var err error
	if status == models.StatusRead {
		err = h.store.MarkRead(ctx, conversationID, userID, upToSequence)
	} else {
		err = h.store.MarkDelivered(ctx, conversationID, userID, upToSequence)
	}
	if err != nil {
		slog.Error("persist receipt failed",
			"conversation_id", conversationID, "user_id", userID, "status", status, "error", err)
		return
	}

	h.mu.RLock()
	members := make([]int64, 0, len(h.members[conversationID]))
	for memberID := range h.members[conversationID] {
		members = append(members, memberID)
	}
	h.mu.RUnlock()

	frame, err := encodeFrame(mustEnvelope(ws.TypeMessageStatus, ws.StatusPayload{
		ConversationID: conversationID,
		UserID:         userID,
		UpToSequence:   upToSequence,
		Status:         status,
	}))
	if err != nil {
		return
	}

	for _, memberID := range members {
		if memberID == userID {
			continue
		}
		h.deliverToUser(memberID, frame)
	}
}

// handleTyping forwards a typing indicator to the other members.
//
// Nothing is persisted: typing is transient, and storing it would create
// write traffic proportional to keystrokes.
func (h *Hub) handleTyping(in inbound) {
	var payload ws.TypingPayload
	if err := json.Unmarshal(in.env.Data, &payload); err != nil {
		h.sendError(in.client, "invalid_payload", "malformed typing payload", "")
		return
	}

	if !in.client.allowTyping(h.cfg.TypingCooldown) {
		return
	}

	userID := in.client.UserID()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	members, err := h.conversationMembers(ctx, payload.ConversationID)
	if err != nil {
		return
	}
	if _, isMember := members[userID]; !isMember {
		return
	}

	frame, err := encodeFrame(mustEnvelope(ws.TypeTyping, ws.TypingEvent{
		ConversationID: payload.ConversationID,
		UserID:         userID,
		Active:         payload.Active,
	}))
	if err != nil {
		return
	}

	for memberID := range members {
		if memberID == userID {
			continue
		}
		h.deliverToUser(memberID, frame)
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

			messages, err := h.store.ListMessagesAfter(syncCtx, conv.ID, from, maxSyncMessagesPerConversation)
			if err != nil {
				slog.Error("sync: list messages failed", "conversation_id", conv.ID, "error", err)
				continue
			}

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
