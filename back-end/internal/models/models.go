// Package models defines the domain types shared by the whole server.
package models

import "time"

// Conversation type values. Stored in the type column and sent as-is to
// the client, so both sides always agree on the spelling.
const (
	ConversationDirect = "direct"
	ConversationGroup  = "group"
)

// Member role values.
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// Message delivery states. pending and failed are client-side only; the
// server tracks the rest through last_delivered and last_read.
const (
	StatusPending   = "pending"
	StatusSent      = "sent"
	StatusDelivered = "delivered"
	StatusRead      = "read"
	StatusFailed    = "failed"
)

// User is a registered account. The password hash is never serialized:
// the json:"-" tag guarantees it cannot leak into an API response.
type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	DisplayName  string    `json:"display_name"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

// Conversation is a direct (1:1) or group chat.
type Conversation struct {
	ID           int64     `json:"id"`
	Type         string    `json:"type"`
	Name         string    `json:"name,omitempty"`
	CreatedBy    int64     `json:"created_by"`
	LastSequence int64     `json:"last_sequence"`
	CreatedAt    time.Time `json:"created_at"`

	// Populated on read, not stored on the conversation row itself.
	Members     []Member `json:"members,omitempty"`
	UnreadCount int      `json:"unread_count"`
	LastMessage *Message `json:"last_message,omitempty"` // nil when the conversation has no message yet
}

// Member links a user to a conversation and tracks how far that user has
// received and read the conversation's messages.
type Member struct {
	ConversationID int64     `json:"conversation_id"`
	UserID         int64     `json:"user_id"`
	Role           string    `json:"role"`
	LastDelivered  int64     `json:"last_delivered"`
	LastRead       int64     `json:"last_read"`
	JoinedAt       time.Time `json:"joined_at"`

	// Populated by joins for display; not columns of this table.
	Username    string `json:"username,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Online      bool   `json:"online"`
}

// Message is a single chat message inside a conversation.
//
// Sequence orders messages within a conversation and is assigned by the
// hub, never by the client. ClientID is the UUID the client generates to
// deduplicate retries after a reconnect.
type Message struct {
	ID             int64     `json:"id"`
	ConversationID int64     `json:"conversation_id"`
	Sequence       int64     `json:"sequence"`
	SenderID       int64     `json:"sender_id"`
	ClientID       string    `json:"client_id"`
	Body           string    `json:"body"`
	CreatedAt      time.Time `json:"created_at"`
}
