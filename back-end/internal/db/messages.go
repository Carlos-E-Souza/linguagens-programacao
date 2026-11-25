package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
)

// messageColumns é a projeção comum a todas as consultas de mensagem.
const messageColumns = `
	id, conversation_id, sequence, sender_id, client_id, body, created_at`

// InsertMessages grava um lote de mensagens numa transação.
//
// ON CONFLICT DO NOTHING mira (sender_id, client_id): um cliente que
// reenvia depois de reconectar pode chegar ao banco duas vezes, e o índice
// único é o que torna isso inofensivo. O resultado de cada mensagem é
// devolvido para que quem chamou distinga uma inserção de uma duplicata.
func (s *Store) InsertMessages(ctx context.Context, messages []models.Message) ([]models.Message, error) {
	if len(messages) == 0 {
		return nil, nil
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

	var stored []models.Message

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

			// Uma duplicata (mesmo remetente e client_id) não afeta
			// nenhuma linha e simplesmente não é devolvida, o que torna o
			// reenvio inofensivo.
			if affected == 0 {
				continue
			}

			id, err := result.LastInsertId()
			if err != nil {
				return fmt.Errorf("insert message: read id: %w", err)
			}
			msg.ID = id
			stored = append(stored, msg)

			// A condição `last_sequence < ?` mantém o contador monotônico
			// mesmo com vários workers confirmando lotes fora de ordem.
			if _, err := tx.ExecContext(ctx, bumpSequence,
				msg.Sequence, msg.ConversationID, msg.Sequence); err != nil {
				return fmt.Errorf("update last_sequence: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return stored, nil
}

// LastSequence devolve a maior sequence atribuída numa conversa, ou 0
// quando ainda não há mensagens. O hub a usa para inicializar seu
// contador em memória ao reiniciar.
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

// ListMessages devolve até limit mensagens de uma conversa, das mais
// novas para as mais antigas, opcionalmente anteriores a beforeSequence.
//
// A consulta anda para trás (sequence < before, DESC) para que a página
// seja a janela mais recente, e depois é invertida em Go para entregar a
// ordem crescente, que é o que a tela de chat desenha.
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

// ListMessagesAfter devolve todas as mensagens com sequence maior que a
// informada, em ordem crescente. É o preenchimento usado quando um
// cliente reconecta e pede o que perdeu.
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

// MarkDelivered avança a marca de entrega do membro.
//
// MAX(atual, novo) torna a atualização monotônica: uma confirmação fora
// de ordem nunca pode mover a marca para trás, o que faria reaparecer um
// indicador já entregue.
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

// MarkRead avança a marca de leitura do membro, e junto a de entrega, já
// que ler implica ter recebido.
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

// Receipt é o estado combinado de confirmação de um membro numa conversa,
// acumulado pelo hub entre duas gravações. Uma marca zero significa "sem
// mudança" para aquela coluna.
type Receipt struct {
	ConversationID int64
	UserID         int64
	Delivered      int64
	Read           int64
}

// ApplyReceipts grava muitas confirmações numa transação.
//
// O hub junta toda confirmação de entrega/leitura que chega numa janela
// curta num único Receipt por membro, então uma rajada de confirmações
// custa uma transação em vez de uma gravação cada. A atualização usa o
// mesmo MAX monotônico de MarkDelivered/MarkRead, e ler continua
// implicando entrega. Um membro que saiu da conversa nesse meio-tempo
// simplesmente não casa com nenhuma linha, o que não é erro aqui.
func (s *Store) ApplyReceipts(ctx context.Context, receipts []Receipt) error {
	if len(receipts) == 0 {
		return nil
	}

	const query = `
		UPDATE conversation_members
		SET last_delivered = MAX(last_delivered, ?, ?),
		    last_read = MAX(last_read, ?)
		WHERE conversation_id = ? AND user_id = ?`

	return s.withTx(ctx, func(tx *sql.Tx) error {
		for _, r := range receipts {
			if _, err := tx.ExecContext(ctx, query,
				r.Delivered, r.Read, r.Read, r.ConversationID, r.UserID); err != nil {
				return fmt.Errorf("apply receipt (conversation %d, user %d): %w",
					r.ConversationID, r.UserID, err)
			}
		}
		return nil
	})
}

// requireAffected converte uma atualização sem linhas afetadas em
// ErrNotFound, para que um status vindo de quem não é membro seja
// informado em vez de ignorado em silêncio.
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

// scanMessages consome um resultado de consulta para um slice.
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

// reverseMessages inverte o slice no lugar, transformando o resultado DESC
// de ListMessages na ordem crescente que a API promete.
func reverseMessages(messages []models.Message) {
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
}
