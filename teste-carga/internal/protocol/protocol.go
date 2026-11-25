// Pacote protocol é uma cópia local do contrato WebSocket do servidor
// (back-end/internal/ws): o envelope de todo frame e os payloads usados pelo
// teste de carga.
//
// A cópia existe porque o Go não permite importar pacotes internal/ de outro
// módulo. Qualquer mudança no protocolo do servidor precisa ser repetida
// aqui; os testes deste pacote usam frames literais para acusar divergência.
package protocol

import (
	"encoding/json"
	"fmt"
	"time"
)

// Tipos de frame do cliente para o servidor.
const (
	TypeMessageSend      = "message.send"
	TypeMessageDelivered = "message.delivered"
	TypeMessageRead      = "message.read"
)

// Tipos de frame do servidor para o cliente.
const (
	TypeMessageAck     = "message.ack"
	TypeMessageNew     = "message.new"
	TypeMessageStatus  = "message.status"
	TypePresence       = "presence"
	TypeError          = "error"
	TypeServerShutdown = "server.shutdown"
)

// Envelope envolve todo frame; Data é decodificado depois que Type é conhecido.
type Envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// NewEnvelope serializa o payload dentro de um envelope pronto para envio.
func NewEnvelope(kind string, payload any) (Envelope, error) {
	if payload == nil {
		return Envelope{Type: kind}, nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, fmt.Errorf("serializar payload de %s: %w", kind, err)
	}
	return Envelope{Type: kind, Data: raw}, nil
}

// SendPayload publica uma mensagem; ClientID volta no ACK e no message.new.
type SendPayload struct {
	ClientID       string `json:"client_id"`
	ConversationID int64  `json:"conversation_id"`
	Body           string `json:"body"`
}

// AckPayload confirma que o servidor aceitou a mensagem.
type AckPayload struct {
	ClientID       string `json:"client_id"`
	ID             int64  `json:"id"`
	ConversationID int64  `json:"conversation_id"`
	Sequence       int64  `json:"sequence"`
	CreatedAt      string `json:"created_at"`
}

// Message é o payload de message.new.
type Message struct {
	ID             int64     `json:"id"`
	ConversationID int64     `json:"conversation_id"`
	Sequence       int64     `json:"sequence"`
	SenderID       int64     `json:"sender_id"`
	ClientID       string    `json:"client_id"`
	Body           string    `json:"body"`
	CreatedAt      time.Time `json:"created_at"`
}

// ReceiptPayload é usado por message.delivered e message.read.
type ReceiptPayload struct {
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

// ErrorPayload descreve um pedido rejeitado pelo servidor.
type ErrorPayload struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	ClientID string `json:"client_id,omitempty"`
}
