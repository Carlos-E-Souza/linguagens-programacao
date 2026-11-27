package cache

import (
	"context"
	"database/sql"
	"errors"
)

// SaveDraft guarda o texto da conversa. Texto vazio apaga o rascunho.
func (c *Cache) SaveDraft(ctx context.Context, conversationID int64, body string) error {
	if body == "" {
		_, err := c.db.ExecContext(ctx, `DELETE FROM drafts WHERE conversation_id = ?`, conversationID)
		return wrap("apagar rascunho", err)
	}
	_, err := c.db.ExecContext(ctx, `INSERT INTO drafts (conversation_id, body) VALUES (?, ?)
		ON CONFLICT (conversation_id) DO UPDATE SET body = excluded.body`, conversationID, body)
	return wrap("salvar rascunho", err)
}

// LoadDraft devolve o texto salvo ou uma string vazia se a conversa não tem rascunho.
func (c *Cache) LoadDraft(ctx context.Context, conversationID int64) (string, error) {
	var body string
	err := c.db.QueryRowContext(ctx, `SELECT body FROM drafts WHERE conversation_id = ?`, conversationID).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return body, wrap("ler rascunho", err)
}
