// Pacote models define os tipos de domínio compartilhados por todo o servidor.
package models

import "time"

// Valores de tipo de conversa. Gravados na coluna type e enviados como
// estão ao cliente, para os dois lados sempre concordarem na grafia.
const (
	ConversationDirect = "direct"
	ConversationGroup  = "group"
)

// Valores de papel do membro.
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// Estados de entrega da mensagem. pending e failed existem só no cliente;
// o servidor acompanha os demais por last_delivered e last_read.
const (
	StatusPending   = "pending"
	StatusSent      = "sent"
	StatusDelivered = "delivered"
	StatusRead      = "read"
	StatusFailed    = "failed"
)

// User é uma conta cadastrada. O hash da senha nunca é serializado: a tag
// json:"-" garante que ele não vaze numa resposta da API.
type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	DisplayName  string    `json:"display_name"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

// Conversation é uma conversa individual (1:1) ou em grupo.
type Conversation struct {
	ID           int64     `json:"id"`
	Type         string    `json:"type"`
	Name         string    `json:"name,omitempty"`
	CreatedBy    int64     `json:"created_by"`
	LastSequence int64     `json:"last_sequence"`
	CreatedAt    time.Time `json:"created_at"`

	// Preenchido na leitura, não gravado na própria linha da conversa.
	Members     []Member `json:"members,omitempty"`
	UnreadCount int      `json:"unread_count"`
	LastMessage *Message `json:"last_message,omitempty"` // nil quando a conversa ainda não tem mensagem
}

// Member liga um usuário a uma conversa e registra até onde esse usuário
// recebeu e leu as mensagens da conversa.
type Member struct {
	ConversationID int64     `json:"conversation_id"`
	UserID         int64     `json:"user_id"`
	Role           string    `json:"role"`
	LastDelivered  int64     `json:"last_delivered"`
	LastRead       int64     `json:"last_read"`
	JoinedAt       time.Time `json:"joined_at"`

	// Preenchidos por joins para exibição; não são colunas desta tabela.
	Username    string `json:"username,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Online      bool   `json:"online"`
}

// Message é uma única mensagem de chat dentro de uma conversa.
//
// Sequence ordena as mensagens dentro de uma conversa e é atribuída pelo
// hub, nunca pelo cliente. ClientID é o UUID que o cliente gera para
// deduplicar as novas tentativas após uma reconexão.
type Message struct {
	ID             int64     `json:"id"`
	ConversationID int64     `json:"conversation_id"`
	Sequence       int64     `json:"sequence"`
	SenderID       int64     `json:"sender_id"`
	ClientID       string    `json:"client_id"`
	Body           string    `json:"body"`
	CreatedAt      time.Time `json:"created_at"`
}
