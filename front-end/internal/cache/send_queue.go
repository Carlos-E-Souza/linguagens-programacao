package cache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/api"
)

// EnqueueSend grava, numa única transação, a mensagem com status pending e a entrada correspondente em send_queue.
// Se a mensagem já existir (nova tentativa de uma que falhou), ela volta para pending e reentra na fila.
func (c *Cache) EnqueueSend(ctx context.Context, msg api.Message) error {
	msg.Status = api.StatusPending
	return c.inTx(ctx, func(tx *sql.Tx) error {
		if err := saveMessage(ctx, tx, msg); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO send_queue (client_id, conversation_id, body, enqueued_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (client_id) DO UPDATE SET enqueued_at = excluded.enqueued_at`,
			msg.ClientID, msg.ConversationID, msg.Body, formatTime(time.Now()))
		return err
	})
}

// RemoveSend tira a mensagem da fila de envio (após ACK ou erro definitivo).
func (c *Cache) RemoveSend(ctx context.Context, clientID string) error {
	_, err := c.db.ExecContext(ctx, `DELETE FROM send_queue WHERE client_id = ?`, clientID)
	return wrap("remover da fila", err)
}

// PendingSends devolve as mensagens da fila na ordem em que foram enfileiradas.
func (c *Cache) PendingSends(ctx context.Context) ([]api.Message, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT q.client_id, q.conversation_id, q.body, COALESCE(m.sender_id, 0), COALESCE(m.created_at, q.enqueued_at)
		FROM send_queue q LEFT JOIN messages m ON m.client_id = q.client_id
		ORDER BY q.enqueued_at, q.rowid`)
	if err != nil {
		return nil, wrap("listar fila", err)
	}
	defer rows.Close()
	var pending []api.Message
	for rows.Next() {
		var msg api.Message
		var createdAt string
		if err := rows.Scan(&msg.ClientID, &msg.ConversationID, &msg.Body, &msg.SenderID, &createdAt); err != nil {
			return nil, wrap("ler fila", err)
		}
		msg.CreatedAt, msg.Status = parseTime(createdAt), api.StatusPending
		pending = append(pending, msg)
	}
	return pending, wrap("listar fila", rows.Err())
}

// AckMessage grava o id, a sequência e o horário do servidor numa mensagem enviada e muda o status para sent
// (sem rebaixar delivered/read). Devolve a mensagem atualizada; ok é false se ela não existir no cache.
func (c *Cache) AckMessage(ctx context.Context, clientID string, id, sequence int64, createdAt time.Time) (msg api.Message, ok bool, err error) {
	err = c.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE messages SET id = ?, sequence = ?, created_at = ?,
			  status = CASE WHEN %s > %s THEN 'sent' ELSE status END
			WHERE client_id = ?`, fmt.Sprintf(statusRank, "'sent'"), fmt.Sprintf(statusRank, "status")),
			id, sequence, formatTime(createdAt), clientID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		ok = true
		var conversationID int64
		if err := tx.QueryRowContext(ctx, `SELECT conversation_id FROM messages WHERE client_id = ?`, clientID).
			Scan(&conversationID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE conversations SET last_sequence = MAX(last_sequence, ?), last_activity = MAX(last_activity, ?)
			WHERE id = ?`, sequence, formatTime(createdAt), conversationID)
		return err
	})
	if err != nil || !ok {
		return api.Message{}, ok, wrap("gravar ACK", err)
	}
	msg, ok, err = c.GetMessage(ctx, clientID)
	return msg, ok, err
}

// GetMessage devolve uma mensagem pelo client_id. ok é false se ela não existir.
func (c *Cache) GetMessage(ctx context.Context, clientID string) (msg api.Message, ok bool, err error) {
	var id, sequence sql.NullInt64
	var createdAt string
	err = c.db.QueryRowContext(ctx, `
		SELECT id, conversation_id, sequence, sender_id, client_id, body, created_at, status
		FROM messages WHERE client_id = ?`, clientID).
		Scan(&id, &msg.ConversationID, &sequence, &msg.SenderID, &msg.ClientID, &msg.Body, &createdAt, &msg.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Message{}, false, nil
	}
	if err != nil {
		return api.Message{}, false, wrap("ler mensagem", err)
	}
	msg.ID, msg.Sequence, msg.CreatedAt = id.Int64, sequence.Int64, parseTime(createdAt)
	return msg, true, nil
}
