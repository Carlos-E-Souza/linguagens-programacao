package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
)

// CreateConversation inserts a conversation together with its members in
// a single transaction.
//
// The transaction is not optional: a conversation without members would
// be unreachable, and a half-created one would be invisible to the very
// users who just created it.
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

		// name is NULL for a direct conversation: the column allows it
		// and an empty string would be a second way to spell "no name".
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

// GetConversation loads one conversation by ID.
func (s *Store) GetConversation(ctx context.Context, id int64) (models.Conversation, error) {
	const query = `
		SELECT id, type, name, created_by, last_sequence, created_at
		FROM conversations
		WHERE id = ?`

	return scanConversation(s.db.QueryRowContext(ctx, query, id))
}

// GetDirectConversation returns the existing 1:1 conversation between two
// users, or ErrNotFound when they have never talked.
//
// The member count comes from a subquery over conversation_members rather
// than from COUNT(*) on the joined rows: the two joins above already
// restrict the result to this pair, so counting joined rows would always
// yield 1 and the HAVING clause would never match.
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

// ListUserConversations returns every conversation the user belongs to,
// with the unread count and the last message, most recently active first.
//
// Unread counts the messages from the other members after what this
// member has read. The member's own messages never count: sending does not
// move the read watermark, so last_sequence - last_read would show a badge
// for messages the user wrote themselves.
//
// "Most recently active" means the time of the last message, falling back
// to the creation time for a conversation without messages. Ordering by
// last_sequence instead would rank conversations by how many messages
// they have, not by when they were last used. Every timestamp is written
// in UTC by the same driver, so comparing them as text is chronological.
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

		// The last-message columns are NULL for an empty conversation
		// because of the LEFT JOIN, hence the nullable destinations.
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

// scanConversation maps one conversation row, tolerating the NULL name of
// a direct conversation and translating sql.ErrNoRows into ErrNotFound.
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

// ListMembers returns the members of a conversation with the display
// fields the client needs, ordered by username for a stable list.
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

// IsMember reports whether the user belongs to the conversation. Every
// message and membership check funnels through this, so authorisation
// has exactly one implementation.
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

// GetMemberRole returns the user's role in the conversation, or
// ErrNotFound when they are not a member.
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

// AddMembers adds users to a conversation, ignoring the ones already
// there so a repeated request is not an error.
//
// ON CONFLICT DO NOTHING leans on the (conversation_id, user_id) primary
// key instead of pre-checking each user, which would race.
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

// RemoveMember removes a user from a conversation.
//
// Removing someone who is not a member is reported as ErrNotFound so the
// handler can answer 404 rather than pretending it worked.
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

// ListContacts returns every user who shares at least one conversation
// with the given user. This is the audience for presence updates.
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
