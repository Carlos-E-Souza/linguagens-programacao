package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
)

// CreateConversation insere uma conversa junto com seus membros numa única
// transação.
//
// A transação não é opcional: uma conversa sem membros seria
// inalcançável, e uma criada pela metade ficaria invisível justamente
// para os usuários que acabaram de criá-la.
func (s *Store) CreateConversation(
	ctx context.Context,
	convType, name string,
	createdBy int64,
	memberIDs []int64,
	roles map[int64]string,
) (models.Conversation, error) {
	if convType != models.ConversationDirect && convType != models.ConversationGroup {
		return models.Conversation{}, fmt.Errorf("create conversation: invalid type %q", convType)
	}

	now := time.Now().UTC()
	var conv models.Conversation

	err := s.withTx(ctx, func(tx *sql.Tx) error {
		const insertConv = `
			INSERT INTO conversations (type, name, created_by, last_sequence, created_at)
			VALUES (?, ?, ?, 0, ?)`

		// name é NULL numa conversa individual: a coluna permite, e uma
		// string vazia seria uma segunda forma de dizer "sem nome".
		var sqlName any
		if name != "" {
			sqlName = name
		}

		result, err := tx.ExecContext(ctx, insertConv, convType, sqlName, createdBy, now)
		if err != nil {
			return fmt.Errorf("insert conversation: %w", err)
		}

		convID, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("insert conversation: read id: %w", err)
		}

		const insertMember = `
			INSERT INTO conversation_members
				(conversation_id, user_id, role, last_delivered, last_read, joined_at)
			VALUES (?, ?, ?, 0, 0, ?)`

		for _, userID := range memberIDs {
			role := roles[userID]
			if role == "" {
				role = models.RoleMember
			}
			if _, err := tx.ExecContext(ctx, insertMember, convID, userID, role, now); err != nil {
				if isForeignKeyViolation(err) {
					return fmt.Errorf("add member %d: %w", userID, ErrNotFound)
				}
				return fmt.Errorf("add member %d: %w", userID, err)
			}
		}

		conv = models.Conversation{
			ID:           convID,
			Type:         convType,
			Name:         name,
			CreatedBy:    createdBy,
			LastSequence: 0,
			CreatedAt:    now,
		}
		return nil
	})
	if err != nil {
		return models.Conversation{}, err
	}

	return conv, nil
}

// GetConversation carrega uma conversa pelo ID.
func (s *Store) GetConversation(ctx context.Context, id int64) (models.Conversation, error) {
	const query = `
		SELECT id, type, name, created_by, last_sequence, created_at
		FROM conversations
		WHERE id = ?`

	return scanConversation(s.db.QueryRowContext(ctx, query, id))
}

// GetDirectConversation devolve a conversa 1:1 existente entre dois
// usuários, ou ErrNotFound quando eles nunca conversaram.
//
// A contagem de membros vem de uma subconsulta em conversation_members e
// não de COUNT(*) sobre as linhas do join: os dois joins acima já
// restringem o resultado a este par, então contar as linhas do join daria
// sempre 1 e a cláusula HAVING nunca casaria.
func (s *Store) GetDirectConversation(ctx context.Context, userA, userB int64) (models.Conversation, error) {
	const query = `
		SELECT c.id, c.type, c.name, c.created_by, c.last_sequence, c.created_at
		FROM conversations c
		JOIN conversation_members ma ON ma.conversation_id = c.id AND ma.user_id = ?
		JOIN conversation_members mb ON mb.conversation_id = c.id AND mb.user_id = ?
		WHERE c.type = ?
		  AND (SELECT COUNT(*) FROM conversation_members m
		       WHERE m.conversation_id = c.id) = 2
		LIMIT 1`

	return scanConversation(s.db.QueryRowContext(ctx, query, userA, userB, models.ConversationDirect))
}

// ListUserConversations devolve todas as conversas de que o usuário
// participa, com a contagem de não lidas e a última mensagem, da atividade
// mais recente para a mais antiga.
//
// As não lidas contam as mensagens dos outros membros depois do que este
// membro já leu. As mensagens do próprio membro nunca contam: enviar não
// move a marca de leitura, então last_sequence - last_read mostraria um
// contador para mensagens que o próprio usuário escreveu.
//
// "Atividade mais recente" é o horário da última mensagem, ou o horário de
// criação numa conversa sem mensagens. Ordenar por last_sequence
// classificaria as conversas pela quantidade de mensagens, não pelo último
// uso. Todo horário é gravado em UTC pelo mesmo driver, então compará-los
// como texto segue a ordem cronológica.
func (s *Store) ListUserConversations(ctx context.Context, userID int64) ([]models.Conversation, error) {
	const query = `
		SELECT
			c.id, c.type, c.name, c.created_by, c.last_sequence, c.created_at,
			(SELECT COUNT(*) FROM messages um
			 WHERE um.conversation_id = c.id AND um.sequence > m.last_read
			   AND um.sender_id != m.user_id) AS unread_count,
			lm.id, lm.sequence, lm.sender_id, lm.client_id, lm.body, lm.created_at
		FROM conversations c
		JOIN conversation_members m ON m.conversation_id = c.id
		LEFT JOIN messages lm ON lm.conversation_id = c.id
			AND lm.sequence = (SELECT MAX(sequence) FROM messages WHERE conversation_id = c.id)
		WHERE m.user_id = ?
		ORDER BY COALESCE(lm.created_at, c.created_at) DESC, c.id DESC`

	rows, err := s.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("list user conversations: %w", err)
	}
	defer rows.Close()

	conversations := make([]models.Conversation, 0)
	for rows.Next() {
		var conv models.Conversation
		var name sql.NullString

		// As colunas da última mensagem são NULL numa conversa vazia por
		// causa do LEFT JOIN, daí os destinos anuláveis.
		var (
			lastID, lastSequence, lastSender sql.NullInt64
			lastClientID, lastBody           sql.NullString
			lastCreatedAt                    sql.NullTime
		)

		err := rows.Scan(&conv.ID, &conv.Type, &name, &conv.CreatedBy,
			&conv.LastSequence, &conv.CreatedAt, &conv.UnreadCount,
			&lastID, &lastSequence, &lastSender, &lastClientID, &lastBody, &lastCreatedAt)
		if err != nil {
			return nil, fmt.Errorf("scan conversation: %w", err)
		}
		conv.Name = name.String

		if lastID.Valid {
			conv.LastMessage = &models.Message{
				ID:             lastID.Int64,
				ConversationID: conv.ID,
				Sequence:       lastSequence.Int64,
				SenderID:       lastSender.Int64,
				ClientID:       lastClientID.String,
				Body:           lastBody.String,
				CreatedAt:      lastCreatedAt.Time,
			}
		}

		conversations = append(conversations, conv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list user conversations: %w", err)
	}

	return conversations, nil
}

// scanConversation lê uma linha de conversa, tolerando o name NULL de uma
// conversa individual e convertendo sql.ErrNoRows em ErrNotFound.
func scanConversation(row rowScanner) (models.Conversation, error) {
	var conv models.Conversation
	var name sql.NullString

	err := row.Scan(&conv.ID, &conv.Type, &name, &conv.CreatedBy,
		&conv.LastSequence, &conv.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Conversation{}, ErrNotFound
	}
	if err != nil {
		return models.Conversation{}, fmt.Errorf("scan conversation: %w", err)
	}

	conv.Name = name.String
	return conv, nil
}

// ListMembers devolve os membros de uma conversa com os campos de exibição
// de que o cliente precisa, ordenados por username para a lista ser
// estável.
func (s *Store) ListMembers(ctx context.Context, conversationID int64) ([]models.Member, error) {
	const query = `
		SELECT
			m.conversation_id, m.user_id, m.role,
			m.last_delivered, m.last_read, m.joined_at,
			u.username, u.display_name
		FROM conversation_members m
		JOIN users u ON u.id = m.user_id
		WHERE m.conversation_id = ?
		ORDER BY u.username`

	rows, err := s.db.QueryContext(ctx, query, conversationID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()

	members := make([]models.Member, 0)
	for rows.Next() {
		var m models.Member
		err := rows.Scan(&m.ConversationID, &m.UserID, &m.Role,
			&m.LastDelivered, &m.LastRead, &m.JoinedAt,
			&m.Username, &m.DisplayName)
		if err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}

	return members, nil
}

// IsMember informa se o usuário participa da conversa. Toda checagem de
// mensagem e de participação passa por aqui, então a autorização tem uma
// única implementação.
func (s *Store) IsMember(ctx context.Context, conversationID, userID int64) (bool, error) {
	const query = `
		SELECT EXISTS (
			SELECT 1 FROM conversation_members
			WHERE conversation_id = ? AND user_id = ?
		)`

	var exists bool
	if err := s.db.QueryRowContext(ctx, query, conversationID, userID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check membership: %w", err)
	}

	return exists, nil
}

// GetMemberRole devolve o papel do usuário na conversa, ou ErrNotFound
// quando ele não é membro.
func (s *Store) GetMemberRole(ctx context.Context, conversationID, userID int64) (string, error) {
	const query = `
		SELECT role FROM conversation_members
		WHERE conversation_id = ? AND user_id = ?`

	var role string
	err := s.db.QueryRowContext(ctx, query, conversationID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get member role: %w", err)
	}

	return role, nil
}

// AddMembers adiciona usuários a uma conversa, ignorando os que já estão
// nela, para que uma requisição repetida não seja erro.
//
// ON CONFLICT DO NOTHING se apoia na chave primária (conversation_id,
// user_id) em vez de checar cada usuário antes, o que geraria corrida.
func (s *Store) AddMembers(ctx context.Context, conversationID int64, userIDs []int64) error {
	if len(userIDs) == 0 {
		return nil
	}

	const query = `
		INSERT INTO conversation_members
			(conversation_id, user_id, role, last_delivered, last_read, joined_at)
		VALUES (?, ?, ?, 0, 0, ?)
		ON CONFLICT (conversation_id, user_id) DO NOTHING`

	now := time.Now().UTC()

	return s.withTx(ctx, func(tx *sql.Tx) error {
		for _, userID := range userIDs {
			if _, err := tx.ExecContext(ctx, query,
				conversationID, userID, models.RoleMember, now); err != nil {
				if isForeignKeyViolation(err) {
					return fmt.Errorf("add member %d: %w", userID, ErrNotFound)
				}
				return fmt.Errorf("add member %d: %w", userID, err)
			}
		}
		return nil
	})
}

// RemoveMember remove um usuário de uma conversa.
//
// Remover quem não é membro é informado como ErrNotFound, para que o
// handler responda 404 em vez de fingir que funcionou.
func (s *Store) RemoveMember(ctx context.Context, conversationID, userID int64) error {
	const query = `
		DELETE FROM conversation_members
		WHERE conversation_id = ? AND user_id = ?`

	result, err := s.db.ExecContext(ctx, query, conversationID, userID)
	if err != nil {
		return fmt.Errorf("remove member: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("remove member: read affected rows: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}

	return nil
}

// ListContacts devolve todos os usuários que compartilham ao menos uma
// conversa com o usuário dado. É o público das atualizações de presença.
func (s *Store) ListContacts(ctx context.Context, userID int64) ([]int64, error) {
	const query = `
		SELECT DISTINCT other.user_id
		FROM conversation_members mine
		JOIN conversation_members other ON other.conversation_id = mine.conversation_id
		WHERE mine.user_id = ? AND other.user_id != ?`

	rows, err := s.db.QueryContext(ctx, query, userID, userID)
	if err != nil {
		return nil, fmt.Errorf("list contacts: %w", err)
	}
	defer rows.Close()

	contacts := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan contact: %w", err)
		}
		contacts = append(contacts, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list contacts: %w", err)
	}

	return contacts, nil
}
