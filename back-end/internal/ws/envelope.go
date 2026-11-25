// Pacote ws define o contrato WebSocket: o envelope que envolve todo frame
// e os tipos de payload dentro dele.
//
// Os nomes aqui são o contrato público compartilhado com o cliente, e
// coincidem com as tags JSON, os campos REST e as colunas do banco. Um
// valor nunca precisa ser renomeado enquanto atravessa o sistema.
package ws

import (
	"encoding/json"
	"fmt"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
)

// Tipos de mensagem do cliente para o servidor.
const (
	TypeMessageSend      = "message.send"
	TypeMessageDelivered = "message.delivered"
	TypeMessageRead      = "message.read"
	TypeSync             = "sync"
)

// Tipos de mensagem do servidor para o cliente.
const (
	TypeMessageAck      = "message.ack"
	TypeMessageNew      = "message.new"
	TypeMessageStatus   = "message.status"
	TypePresence        = "presence"
	TypeConversationUpd = "conversation.updated"
	TypeSyncEnd         = "sync.end"
	TypeError           = "error"
	TypeServerShutdown  = "server.shutdown"
)

// Envelope envolve todo frame. Data guarda JSON bruto para que um frame só
// seja decodificado no tipo de payload certo depois que seu Type é
// conhecido.
type Envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// NewEnvelope serializa um payload num envelope pronto para envio.
func NewEnvelope(msgType string, payload any) (Envelope, error) {
	if payload == nil {
		return Envelope{Type: msgType}, nil
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, fmt.Errorf("marshal %s payload: %w", msgType, err)
	}

	return Envelope{Type: msgType, Data: raw}, nil
}

// SendPayload é o que o cliente envia para publicar uma mensagem.
//
// ClientID é gerado pelo cliente e devolvido no ACK, e é assim que o
// cliente relaciona os dois e que o servidor deduplica um reenvio após uma
// reconexão.
type SendPayload struct {
	ClientID       string `json:"client_id"`
	ConversationID int64  `json:"conversation_id"`
	Body           string `json:"body"`
}

// AckPayload confirma que uma mensagem foi aceita.
//
// Um reenvio duplicado recebe o mesmo ACK do original, então o cliente
// não percebe a diferença e nem precisa.
type AckPayload struct {
	ClientID       string `json:"client_id"`
	ID             int64  `json:"id"`
	ConversationID int64  `json:"conversation_id"`
	Sequence       int64  `json:"sequence"`
	CreatedAt      string `json:"created_at"`
}

// ConversationSequence indica até onde um cliente avançou numa conversa.
type ConversationSequence struct {
	ConversationID int64 `json:"conversation_id"`
	LastSequence   int64 `json:"last_sequence"`
}

// SyncPayload pede tudo o que foi perdido enquanto estava offline.
type SyncPayload struct {
	Conversations []ConversationSequence `json:"conversations"`
}

// ReadReceiptPayload é enviado tanto para confirmações de entrega quanto
// de leitura; o tipo do envelope as distingue.
type ReadReceiptPayload struct {
	ConversationID int64 `json:"conversation_id"`
	UpToSequence   int64 `json:"up_to_sequence"`
}

// StatusPayload informa ao remetente até onde os destinatários chegaram.
type StatusPayload struct {
	ConversationID int64  `json:"conversation_id"`
	UserID         int64  `json:"user_id"`
	UpToSequence   int64  `json:"up_to_sequence"`
	Status         string `json:"status"`
}

// PresencePayload informa que um contato entrou ou saiu.
type PresencePayload struct {
	UserID int64 `json:"user_id"`
	Online bool  `json:"online"`
}

// ConversationUpdatedPayload anuncia uma mudança de participação.
type ConversationUpdatedPayload struct {
	Conversation models.Conversation `json:"conversation"`
}

// ErrorPayload informa uma requisição rejeitada.
//
// ClientID é opcional e só aparece quando a falha pode ser ligada a uma
// mensagem específica enviada pelo cliente, para que ele a marque como
// falha.
type ErrorPayload struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	ClientID string `json:"client_id,omitempty"`
}
