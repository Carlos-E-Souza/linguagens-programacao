package cache

import (
	"context"
	"database/sql"
	"errors"
)

// SyncCursor devolve até onde a sincronização foi concluída. Bancos criados antes da Fase 8
// começam na última mensagem que já estava guardada, sem baixar todo o histórico antigo.
func (c *Cache) SyncCursor(ctx context.Context, conversationID int64) (int64, error) {
	var sequence int64
	err := c.db.QueryRowContext(ctx, `SELECT last_sequence FROM sync_cursors WHERE conversation_id = ?`,
		conversationID).Scan(&sequence)
	if err == nil {
		return sequence, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, wrap("ler cursor de sincronização", err)
	}
	sequence, err = c.LastSequence(ctx, conversationID)
	if err != nil {
		return 0, err
	}
	_, err = c.db.ExecContext(ctx, `INSERT INTO sync_cursors (conversation_id, last_sequence)
		VALUES (?, ?) ON CONFLICT (conversation_id) DO NOTHING`, conversationID, sequence)
	return sequence, wrap("iniciar cursor de sincronização", err)
}

// SaveSyncCursor só é chamada depois que todo o intervalo foi recuperado.
func (c *Cache) SaveSyncCursor(ctx context.Context, conversationID, sequence int64) error {
	_, err := c.db.ExecContext(ctx, `INSERT INTO sync_cursors (conversation_id, last_sequence) VALUES (?, ?)
		ON CONFLICT (conversation_id) DO UPDATE SET last_sequence = MAX(sync_cursors.last_sequence, excluded.last_sequence)`,
		conversationID, sequence)
	return wrap("salvar cursor de sincronização", err)
}
