package api

import "time"

// Tipos de conversa.
const (
	ConversationDirect = "direct"
	ConversationGroup  = "group"
)

// Status de uma mensagem no cliente. O servidor não envia status no histórico; ele é calculado no cliente.
const (
	StatusPending   = "pending"
	StatusSent      = "sent"
	StatusDelivered = "delivered"
	StatusRead      = "read"
	StatusFailed    = "failed"
)

// User é um usuário do chat, com as mesmas tags JSON do servidor.
type User struct {
	ID          int64     `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Online      bool      `json:"online"`
	CreatedAt   time.Time `json:"created_at"`
}

// Member é um membro de uma conversa.
type Member struct {
	UserID        int64     `json:"user_id"`
	Username      string    `json:"username"`
	DisplayName   string    `json:"display_name"`
	Role          string    `json:"role"`
	LastDelivered int64     `json:"last_delivered"`
	LastRead      int64     `json:"last_read"`
	Online        bool      `json:"online"`
	JoinedAt      time.Time `json:"joined_at"`
}

// Conversation é uma conversa individual ou em grupo, com membros, última mensagem e contador de não lidas.
type Conversation struct {
	ID           int64     `json:"id"`
	Type         string    `json:"type"`
	Name         string    `json:"name"`
	CreatedBy    int64     `json:"created_by"`
	LastSequence int64     `json:"last_sequence"`
	CreatedAt    time.Time `json:"created_at"`
	Members      []Member  `json:"members"`
	LastMessage  *Message  `json:"last_message"`
	UnreadCount  int64     `json:"unread_count"`
}

// Message é uma mensagem de uma conversa. Status só existe no cliente.
type Message struct {
	ID             int64     `json:"id"`
	ConversationID int64     `json:"conversation_id"`
	Sequence       int64     `json:"sequence"`
	SenderID       int64     `json:"sender_id"`
	ClientID       string    `json:"client_id"`
	Body           string    `json:"body"`
	CreatedAt      time.Time `json:"created_at"`
	Status         string    `json:"status,omitempty"`
}

// LoginResponse é a resposta de POST /api/auth/login.
type LoginResponse struct {
	Token string `json:"token"`
	User  User   `json:"user"`
}
