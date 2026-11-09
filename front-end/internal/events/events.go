package events

import "time"

// Nomes dos eventos emitidos para a interface. A interface os escuta com EventsOn (wailsjs/runtime) em bridge.js.
const (
	// ConnectionState informa o estado da conexão WebSocket (ver os valores State*).
	ConnectionState = "connection:state"
	// MessageNew traz uma mensagem completa recebida do servidor.
	MessageNew = "message:new"
	// MessageUpdated traz a confirmação (ACK) ou a falha definitiva de uma mensagem enviada.
	MessageUpdated = "message:updated"
	// MessageStatus informa que um membro confirmou entrega ou leitura até uma sequência.
	MessageStatus = "message:status"
	// ConversationUpdated traz uma conversa com membros e contador de não lidas.
	ConversationUpdated = "conversation:updated"
	// ConversationRemoved informa que a conversa deixou de existir para o usuário.
	ConversationRemoved = "conversation:removed"
	// Presence informa que um contato entrou ou saiu.
	Presence = "presence"
	// Typing informa que um membro começou ou parou de digitar.
	Typing = "typing"
	// SessionExpired avisa que o token não é mais válido; a interface volta para o login.
	SessionExpired = "session:expired"
	// Error leva uma mensagem de erro amigável para a interface.
	Error = "error"
)

// Valores do evento ConnectionState.
const (
	StateConnected    = "connected"
	StateConnecting   = "connecting"
	StateDisconnected = "disconnected"
)

// Emitter é a função usada para emitir eventos. Na aplicação ela envolve runtime.EventsEmit; nos testes pode ser
// substituída por uma função que apenas registra as chamadas.
type Emitter func(name string, data ...any)

// MessageUpdatedPayload é o payload de MessageUpdated.
type MessageUpdatedPayload struct {
	ConversationID int64     `json:"conversation_id"`
	ClientID       string    `json:"client_id"`
	ID             int64     `json:"id"`
	Sequence       int64     `json:"sequence"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	// Error explica por que a mensagem falhou (só com status failed).
	Error string `json:"error,omitempty"`
}

// MessageStatusPayload é o payload de MessageStatus.
type MessageStatusPayload struct {
	ConversationID int64  `json:"conversation_id"`
	UserID         int64  `json:"user_id"`
	UpToSequence   int64  `json:"up_to_sequence"`
	Status         string `json:"status"`
}

// ConversationRemovedPayload é o payload de ConversationRemoved.
type ConversationRemovedPayload struct {
	ConversationID int64 `json:"conversation_id"`
}

// PresencePayload é o payload de Presence.
type PresencePayload struct {
	UserID int64 `json:"user_id"`
	Online bool  `json:"online"`
}

// TypingPayload é o payload de Typing.
type TypingPayload struct {
	ConversationID int64 `json:"conversation_id"`
	UserID         int64 `json:"user_id"`
	Active         bool  `json:"active"`
}

// ErrorPayload é o payload de Error.
type ErrorPayload struct {
	Message string `json:"message"`
}
