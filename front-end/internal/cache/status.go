package cache

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/api"
)

// execer é o que *sql.DB e *sql.Tx têm em comum para os comandos deste arquivo.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// sessionUser é o usuário dono do cache, lido da sessão salva. Sem sessão, os comandos que dependem dele não
// alteram nada (comparações com NULL são falsas).
const sessionUser = `(SELECT user_id FROM session WHERE id = 1)`

// Receipts é a posição que todos os outros membros já alcançaram em uma conversa: até Delivered todos receberam
// e até Read todos leram. Zero significa que nenhuma mensagem chegou a esse ponto.
type Receipts struct {
	Delivered int64
	Read      int64
}

// ApplyReceipt grava a confirmação de entrega ou leitura de um membro (as posições nunca voltam) e recalcula o
// status das mensagens do dono do cache. Ler implica ter recebido. Devolve a posição comum aos outros membros.
func (c *Cache) ApplyReceipt(ctx context.Context, conversationID, userID, upTo int64, status string) (Receipts, error) {
	var receipts Receipts
	err := c.inTx(ctx, func(tx *sql.Tx) error {
		query := `UPDATE members SET last_delivered = MAX(last_delivered, ?) WHERE conversation_id = ? AND user_id = ?`
		if status == api.StatusRead {
			query = `UPDATE members SET last_delivered = MAX(last_delivered, ?1), last_read = MAX(last_read, ?1)
				WHERE conversation_id = ?2 AND user_id = ?3`
		}
		if _, err := tx.ExecContext(ctx, query, upTo, conversationID, userID); err != nil {
			return err
		}
		var err error
		receipts, err = refreshOwnStatuses(ctx, tx, conversationID)
		return err
	})
	return receipts, err
}

// refreshOwnStatuses promove as mensagens do dono do cache para delivered/read conforme as posições dos outros
// membros: em grupos, só quando todos confirmaram. O status nunca regride e failed/pending não mudam.
func refreshOwnStatuses(ctx context.Context, db execer, conversationID int64) (Receipts, error) {
	var delivered, read sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT MIN(last_delivered), MIN(last_read) FROM members
		WHERE conversation_id = ? AND user_id != `+sessionUser, conversationID).Scan(&delivered, &read)
	if err != nil {
		return Receipts{}, err
	}
	receipts := Receipts{Delivered: delivered.Int64, Read: read.Int64}
	if !delivered.Valid {
		return receipts, nil // ninguém além do dono do cache
	}
	if _, err := db.ExecContext(ctx, `UPDATE messages SET status = 'read'
		WHERE conversation_id = ? AND sender_id = `+sessionUser+` AND sequence <= ? AND status IN ('sent', 'delivered')`,
		conversationID, receipts.Read); err != nil {
		return Receipts{}, err
	}
	if _, err := db.ExecContext(ctx, `UPDATE messages SET status = 'delivered'
		WHERE conversation_id = ? AND sender_id = `+sessionUser+` AND sequence <= ? AND status = 'sent'`,
		conversationID, receipts.Delivered); err != nil {
		return Receipts{}, err
	}
	return receipts, nil
}

// MarkRead registra que o usuário leu a conversa até upTo e recalcula as não lidas: mensagens de outros com
// sequência maior que a última lida.
func (c *Cache) MarkRead(ctx context.Context, conversationID, userID, upTo int64) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE members SET last_delivered = MAX(last_delivered, ?1),
			last_read = MAX(last_read, ?1) WHERE conversation_id = ?2 AND user_id = ?3`,
			upTo, conversationID, userID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE conversations SET unread_count = (
			SELECT COUNT(*) FROM messages m WHERE m.conversation_id = conversations.id AND m.sender_id != ?1
			  AND m.sequence > MAX(?2, COALESCE((SELECT last_read FROM members
			    WHERE conversation_id = conversations.id AND user_id = ?1), 0)))
			WHERE id = ?3`, userID, upTo, conversationID)
		return err
	})
}

// SaveReceived grava mensagens recebidas do servidor e soma às não lidas as que são de outros membros e ainda
// não estavam no cache, tudo na mesma transação (a mesma mensagem pode chegar ao vivo e pelo REST ao mesmo tempo).
// Devolve quantas eram novas.
func (c *Cache) SaveReceived(ctx context.Context, messages []api.Message, userID int64) (int, error) {
	fresh := 0
	err := c.inTx(ctx, func(tx *sql.Tx) error {
		fresh = 0
		touched := make(map[int64]bool)
		for _, msg := range messages {
			var one int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM messages WHERE conversation_id = ? AND sequence = ?`,
				msg.ConversationID, msg.Sequence).Scan(&one)
			known := err == nil
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err := saveMessage(ctx, tx, msg); err != nil {
				return err
			}
			touched[msg.ConversationID] = true
			if known {
				continue
			}
			fresh++
			if msg.SenderID != userID {
				if _, err := tx.ExecContext(ctx, `UPDATE conversations SET unread_count = unread_count + 1
					WHERE id = ?`, msg.ConversationID); err != nil {
					return err
				}
			}
		}
		for id := range touched {
			if _, err := refreshOwnStatuses(ctx, tx, id); err != nil {
				return err
			}
		}
		return nil
	})
	return fresh, wrap("salvar mensagens recebidas", err)
}

// SetPresence atualiza o indicador online do usuário em todas as conversas.
func (c *Cache) SetPresence(ctx context.Context, userID int64, online bool) error {
	_, err := c.db.ExecContext(ctx, `UPDATE members SET online = ? WHERE user_id = ?`, online, userID)
	return wrap("atualizar presença", err)
}
