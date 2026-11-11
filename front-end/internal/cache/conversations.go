package cache

import (
	"context"
	"database/sql"

	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/api"
)

// ApplyConversationUpdate grava o que conversation.updated traz (nome, tipo e membros com suas posições), sem
// mexer nas não lidas nem na última mensagem, que o evento não informa. O servidor também não informa a presença
// dos membros nesse evento: quem já aparece em outra conversa mantém o indicador conhecido. Uma conversa nova
// entra com o cursor de sincronização na última sequência: o histórico anterior é buscado sob demanda por
// LoadMessages, e a sincronização só precisa recuperar o que chegar depois. Devolve se a conversa já existia.
func (c *Cache) ApplyConversationUpdate(ctx context.Context, conv api.Conversation) (existed bool, err error) {
	err = c.inTx(ctx, func(tx *sql.Tx) error {
		var one int
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM conversations WHERE id = ?`, conv.ID).Scan(&one); err {
		case nil:
			existed = true
		case sql.ErrNoRows:
		default:
			return err
		}

		online := make(map[int64]bool)
		rows, err := tx.QueryContext(ctx, `SELECT user_id, MAX(online) FROM members GROUP BY user_id`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			var on bool
			if err := rows.Scan(&id, &on); err != nil {
				rows.Close()
				return err
			}
			online[id] = on
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO conversations (id, type, name, created_by, last_sequence, unread_count, created_at, last_activity)
			VALUES (?, ?, ?, ?, ?, 0, ?, ?)
			ON CONFLICT (id) DO UPDATE SET type = excluded.type, name = excluded.name,
			  created_by = excluded.created_by,
			  last_sequence = MAX(conversations.last_sequence, excluded.last_sequence)`,
			conv.ID, conv.Type, conv.Name, conv.CreatedBy, conv.LastSequence,
			formatTime(conv.CreatedAt), formatTime(conv.CreatedAt))
		if err != nil {
			return err
		}
		if !existed {
			if _, err := tx.ExecContext(ctx, `INSERT INTO sync_cursors (conversation_id, last_sequence) VALUES (?, ?)
				ON CONFLICT (conversation_id) DO NOTHING`, conv.ID, conv.LastSequence); err != nil {
				return err
			}
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM members WHERE conversation_id = ?`, conv.ID); err != nil {
			return err
		}
		for _, m := range conv.Members {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO members (conversation_id, user_id, username, display_name, role, last_delivered, last_read,
				  online, joined_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				conv.ID, m.UserID, m.Username, m.DisplayName, m.Role, m.LastDelivered, m.LastRead,
				m.Online || online[m.UserID], formatTime(m.JoinedAt))
			if err != nil {
				return err
			}
		}
		_, err = refreshOwnStatuses(ctx, tx, conv.ID)
		return err
	})
	return existed, wrap("atualizar conversa", err)
}

// DeleteConversation apaga a conversa com membros, mensagens e cursor (pelas chaves estrangeiras) e tira da fila
// de envio o que ainda ia para ela: o usuário saiu ou foi removido.
func (c *Cache) DeleteConversation(ctx context.Context, id int64) error {
	return wrap("apagar conversa", c.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM send_queue WHERE conversation_id = ?`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM conversations WHERE id = ?`, id)
		return err
	}))
}
