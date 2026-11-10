package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
)

// messageColumns is the projection shared by every message query.
const messageColumns = `
	id, conversation_id, sequence, sender_id, client_id, body, created_at`

// InsertMessages writes a batch of messages in one transaction.
//
// ON CONFLICT DO NOTHING targets (sender_id, client_id): a client that
// resends after a reconnect may reach the database twice, and the unique
// index is what makes that harmless. The per-message outcome is returned
// so the caller can tell an insert from a duplicate.
func (s *Store) InsertMessages(ctx context.Context, messages []models.Message) (int, error) {
	if len(messages) == 0 {
		return 0, nil
	}

	const insertMsg = `
		INSERT INTO messages
			(conversation_id, sequence, sender_id, client_id, body, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (sender_id, client_id) DO NOTHING`

	const bumpSequence = `
		UPDATE conversations
		SET last_sequence = ?
		WHERE id = ? AND last_sequence < ?`

	inserted := 0

	err := s.withTx(ctx, func(tx *sql.Tx) error {
		for _, msg := range messages {
			result, err := tx.ExecContext(ctx, insertMsg,
				msg.ConversationID, msg.Sequence, msg.SenderID,
				msg.ClientID, msg.Body, msg.CreatedAt)
			if err != nil {
				return fmt.Errorf("insert message: %w", err)
			}

			affected, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("insert message: read affected rows: %w", err)
			}
			inserted += int(affected)

			// The guard `last_sequence < ?` keeps the counter monotonic
			// even though several workers commit batches out of order.
			if _, err := tx.ExecContext(ctx, bumpSequence,
				msg.Sequence, msg.ConversationID, msg.Sequence); err != nil {
				return fmt.Errorf("update last_sequence: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	return inserted, nil
}

// LastSequence returns the highest sequence assigned in a conversation,
// or 0 when there are no messages yet. The hub uses it to prime its
// in-memory counter on restart.
func (s *Store) LastSequence(ctx context.Context, conversationID int64) (int64, error) {
	const query = `SELECT last_sequence FROM conversations WHERE id = ?`

	var sequence int64
	err := s.db.QueryRowContext(ctx, query, conversationID).Scan(&sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("read last sequence: %w", err)
	}

	return sequence, nil
}

// ListMessages returns up to limit messages from a conversation,
// newest first, optionally older than beforeSequence.
//
// The query walks backwards (sequence < before, DESC) so that the page
// is the most recent window, then reverses in Go to hand the caller
// ascending order, which is what a chat view renders.
func (s *Store) ListMessages(ctx context.Context, conversationID, beforeSequence int64, limit int) ([]models.Message, error) {
	query := `
		SELECT ` + messageColumns + `
		FROM messages
		WHERE conversation_id = ?
		  AND (? = 0 OR sequence < ?)
		ORDER BY sequence DESC
		LIMIT ?`

	rows, err := s.db.QueryContext(ctx, query, conversationID, beforeSequence, beforeSequence, limit)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	defer rows.Close()

	messages, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}

	reverseMessages(messages)
	return messages, nil
}

// ListMessagesAfter returns every message with a sequence greater than
// the given one, in ascending order. This is the backfill used when a
// client reconnects and asks for what it missed.
func (s *Store) ListMessagesAfter(ctx context.Context, conversationID, afterSequence int64, limit int) ([]models.Message, error) {
	query := `
		SELECT ` + messageColumns + `
		FROM messages
		WHERE conversation_id = ? AND sequence > ?
		ORDER BY sequence
		LIMIT ?`

	rows, err := s.db.QueryContext(ctx, query, conversationID, afterSequence, limit)
	if err != nil {
		return nil, fmt.Errorf("list messages after: %w", err)
	}
	defer rows.Close()

	return scanMessages(rows)
}

// MarkDelivered advances the member's delivered watermark.
//
// MAX(current, new) makes the update monotonic: an out-of-order
// acknowledgement can never move the watermark backwards, which would
// otherwise resurrect an already-delivered badge.
func (s *Store) MarkDelivered(ctx context.Context, conversationID, userID, upToSequence int64) error {
	const query = `
		UPDATE conversation_members
		SET last_delivered = MAX(last_delivered, ?)
		WHERE conversation_id = ? AND user_id = ?`

	result, err := s.db.ExecContext(ctx, query, upToSequence, conversationID, userID)
	if err != nil {
		return fmt.Errorf("mark delivered: %w", err)
	}

	return requireAffected(result, "mark delivered")
}

// MarkRead advances the member's read watermark, and the delivered one
// with it, since reading implies delivery.
func (s *Store) MarkRead(ctx context.Context, conversationID, userID, upToSequence int64) error {
	const query = `
		UPDATE conversation_members
		SET last_read = MAX(last_read, ?),
		    last_delivered = MAX(last_delivered, ?)
		WHERE conversation_id = ? AND user_id = ?`

	result, err := s.db.ExecContext(ctx, query, upToSequence, upToSequence, conversationID, userID)
	if err != nil {
		return fmt.Errorf("mark read: %w", err)
	}

	return requireAffected(result, "mark read")
}

// requireAffected turns a zero-row update into ErrNotFound, so a status
// update from a non-member is reported instead of silently ignored.
func requireAffected(result sql.Result, action string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: read affected rows: %w", action, err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// scanMessages drains a result set into a slice.
func scanMessages(rows *sql.Rows) ([]models.Message, error) {
	messages := make([]models.Message, 0)
	for rows.Next() {
		var msg models.Message
		err := rows.Scan(&msg.ID, &msg.ConversationID, &msg.Sequence,
			&msg.SenderID, &msg.ClientID, &msg.Body, &msg.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan messages: %w", err)
	}

	return messages, nil
}

// reverseMessages flips the slice in place, turning the DESC result of
// ListMessages into the ascending order the API promises.
func reverseMessages(messages []models.Message) {
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
}
