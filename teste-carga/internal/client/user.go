package client

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/Carlos-E-Souza/linguagens-programacao/teste-carga/internal/metrics"
	"github.com/Carlos-E-Souza/linguagens-programacao/teste-carga/internal/protocol"
)

// writeTimeout limita cada escrita no WebSocket.
const writeTimeout = 5 * time.Second

// receiptFlushInterval agrupa os recibos antes de enviá-los. Recibos são
// cumulativos (up_to_sequence), então basta o maior de cada conversa.
const receiptFlushInterval = 50 * time.Millisecond

// UserConversation é uma conversa do ponto de vista de um usuário;
// Recipients são os ids dos outros membros.
type UserConversation struct {
	ID         int64
	Kind       string
	Recipients []int64
}

// User é um usuário simulado com uma conexão WebSocket.
type User struct {
	ID            int64
	Token         string
	Conversations []UserConversation

	conn     *websocket.Conn
	writeMu  sync.Mutex // leitor (recibos) e enviador escrevem na mesma conexão
	closing  atomic.Bool
	done     chan struct{}
	rec      *metrics.Recorder
	receipts bool

	// O leitor só anota o maior sequence por conversa; receiptWriter envia.
	receiptMu      sync.Mutex
	pendingReceipt map[int64]int64
	receiptWake    chan struct{}
}

// Connect abre /ws autenticado e inicia a goroutine leitora.
func (u *User) Connect(ctx context.Context, wsURL string, rec *metrics.Recorder, receipts bool) error {
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + u.Token}},
	})
	if err != nil {
		return err
	}
	conn.SetReadLimit(1 << 20)
	u.conn, u.rec, u.receipts = conn, rec, receipts
	u.done = make(chan struct{})
	u.pendingReceipt = map[int64]int64{}
	u.receiptWake = make(chan struct{}, 1)
	go u.readLoop()
	if receipts {
		go u.receiptWriter()
	}
	return nil
}

// Send publica uma mensagem nova na conversa e a registra no coletor.
//
// ctx só decide se o envio começa: a escrita usa um contexto próprio, porque
// o coder/websocket fecha a conexão inteira quando o contexto de uma escrita
// em andamento acaba. O instante do envio é marcado depois de obter a vez de
// escrever, para a espera local não contar como latência do servidor.
func (u *User) Send(ctx context.Context, conv UserConversation, rec *metrics.Recorder) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	clientID := uuid.NewString()
	env, err := protocol.NewEnvelope(protocol.TypeMessageSend, protocol.SendPayload{
		ClientID: clientID, ConversationID: conv.ID, Body: "carga " + clientID,
	})
	if err != nil {
		return err
	}
	return u.write(env, func() {
		rec.Sent(clientID, conv.ID, conv.Kind, u.ID, conv.Recipients, time.Now())
	})
}

// Close encerra a conexão sem contar como desconexão e espera o leitor sair.
func (u *User) Close() {
	if u.conn == nil || !u.closing.CompareAndSwap(false, true) {
		return
	}
	u.writeMu.Lock()
	_ = u.conn.Close(websocket.StatusNormalClosure, "fim do teste")
	u.writeMu.Unlock()
	<-u.done
}

// write envia um frame; beforeWrite (se não nil) roda já com a vez de escrever.
func (u *User) write(env protocol.Envelope, beforeWrite func()) error {
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	u.writeMu.Lock()
	defer u.writeMu.Unlock()
	if beforeWrite != nil {
		beforeWrite()
	}
	return u.conn.Write(ctx, websocket.MessageText, data)
}

// readLoop drena a conexão sem parar: o servidor derruba clientes lentos.
func (u *User) readLoop() {
	defer close(u.done)
	ctx := context.Background()
	for {
		_, data, err := u.conn.Read(ctx)
		if err != nil {
			u.lost()
			return
		}
		at := time.Now()
		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			u.rec.Error("invalid_frame")
			continue
		}
		switch env.Type {
		case protocol.TypeMessageAck:
			var p protocol.AckPayload
			if json.Unmarshal(env.Data, &p) == nil {
				u.rec.Acked(p.ClientID, at)
			}
		case protocol.TypeMessageNew:
			var m protocol.Message
			if json.Unmarshal(env.Data, &m) != nil {
				u.rec.Error("invalid_frame")
				continue
			}
			u.rec.Received(u.ID, m, at)
			if u.receipts && m.SenderID != u.ID {
				u.queueReceipt(m)
			}
		case protocol.TypeMessageStatus:
			var p protocol.StatusPayload
			if json.Unmarshal(env.Data, &p) == nil {
				u.rec.Status(p.Status)
			}
		case protocol.TypeError:
			var p protocol.ErrorPayload
			if json.Unmarshal(env.Data, &p) == nil {
				u.rec.Error(p.Code)
			}
		case protocol.TypeServerShutdown:
			u.lost()
			_ = u.conn.CloseNow()
			return
		}
	}
}

// lost conta a desconexão, exceto quando o próprio teste fechou a conexão.
func (u *User) lost() {
	if u.closing.CompareAndSwap(false, true) {
		u.rec.Disconnected(u.ID)
	}
}

// queueReceipt anota o recibo sem bloquear o leitor.
func (u *User) queueReceipt(m protocol.Message) {
	u.receiptMu.Lock()
	if m.Sequence > u.pendingReceipt[m.ConversationID] {
		u.pendingReceipt[m.ConversationID] = m.Sequence
	}
	u.receiptMu.Unlock()
	select {
	case u.receiptWake <- struct{}{}:
	default:
	}
}

// receiptWriter envia os recibos agrupados até a conexão acabar.
func (u *User) receiptWriter() {
	for {
		select {
		case <-u.done:
			return
		case <-u.receiptWake:
		}
		select {
		case <-u.done:
			return
		case <-time.After(receiptFlushInterval):
		}
		u.receiptMu.Lock()
		batch := u.pendingReceipt
		u.pendingReceipt = map[int64]int64{}
		u.receiptMu.Unlock()
		for conv, seq := range batch {
			payload := protocol.ReceiptPayload{ConversationID: conv, UpToSequence: seq}
			for _, kind := range []string{protocol.TypeMessageDelivered, protocol.TypeMessageRead} {
				env, _ := protocol.NewEnvelope(kind, payload)
				if err := u.write(env, nil); err != nil {
					if !u.closing.Load() {
						u.rec.Error("receipt_failed")
					}
					return
				}
			}
		}
	}
}
