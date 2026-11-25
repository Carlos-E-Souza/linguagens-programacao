package hub

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/db"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/ws"
)

// handleIncoming despacha um frame de um cliente.
//
// Roda na goroutine do hub, então não pode bloquear: as únicas operações
// permitidas aqui são buscas em mapa, atribuição de sequence e envios por
// canal. Tudo o que envolve banco ou rede acontece em outra goroutine.
func (h *Hub) handleIncoming(ctx context.Context, in inbound) {
	switch in.env.Type {
	case ws.TypeMessageSend:
		h.handleMessageSend(ctx, in)
	case ws.TypeMessageDelivered:
		h.handleReceipt(ctx, in, models.StatusDelivered)
	case ws.TypeMessageRead:
		h.handleReceipt(ctx, in, models.StatusRead)
	case ws.TypeSync:
		h.handleSync(ctx, in)
	default:
		h.sendError(in.client, "unknown_type", "unsupported message type", "")
	}
}

// handleMessageSend valida, atribui uma sequence, confirma e distribui a
// mensagem aos membros da conversa.
func (h *Hub) handleMessageSend(ctx context.Context, in inbound) {
	var payload ws.SendPayload
	if err := json.Unmarshal(in.env.Data, &payload); err != nil {
		h.sendError(in.client, "invalid_payload", "malformed message payload", "")
		return
	}

	senderID := in.client.UserID()
	payload.ClientID = strings.TrimSpace(payload.ClientID)
	payload.Body = strings.TrimSpace(payload.Body)

	if payload.ClientID == "" {
		h.sendError(in.client, "missing_client_id", "client_id is required", "")
		return
	}
	if payload.ConversationID <= 0 {
		h.sendError(in.client, "invalid_conversation_id", "conversation_id is required", payload.ClientID)
		return
	}
	if payload.Body == "" {
		h.sendError(in.client, "empty_body", "message body must not be empty", payload.ClientID)
		return
	}
	if len([]rune(payload.Body)) > h.cfg.MessageLimitChars {
		h.sendError(in.client, "body_too_long",
			"message body exceeds the maximum length", payload.ClientID)
		return
	}

	// Deduplicação: um cliente que não recebeu o ACK reenvia o mesmo
	// client_id e precisa receber a mesma resposta em vez de uma segunda
	// cópia da mensagem.
	key := ackKey{senderID: senderID, clientID: payload.ClientID}
	if previous, ok := h.acks[key]; ok {
		frame, err := encodeFrame(mustEnvelope(ws.TypeMessageAck, previous))
		if err == nil {
			in.client.trySend(frame)
		}
		return
	}

	members, err := h.conversationMembers(ctx, payload.ConversationID)
	if err != nil {
		h.sendError(in.client, "conversation_not_found", "conversation not found", payload.ClientID)
		return
	}

	if _, isMember := members[senderID]; !isMember {
		// Responder como se a conversa não existisse evita revelar a
		// existência dela a alguém de fora.
		h.sendError(in.client, "not_a_member", "you are not a member of this conversation", payload.ClientID)
		return
	}

	// Run é o único escritor, então incrementar o contador aqui não gera
	// corrida. Na primeira mensagem de uma conversa o valor é inicializado
	// a partir do banco.
	sequence, err := h.nextSequence(ctx, payload.ConversationID)
	if err != nil {
		h.sendError(in.client, "internal_error", "could not assign a sequence", payload.ClientID)
		return
	}

	now := time.Now().UTC()
	msg := models.Message{
		ConversationID: payload.ConversationID,
		Sequence:       sequence,
		SenderID:       senderID,
		ClientID:       payload.ClientID,
		Body:           payload.Body,
		CreatedAt:      now,
	}

	ack := ws.AckPayload{
		ClientID:       payload.ClientID,
		Sequence:       sequence,
		ConversationID: payload.ConversationID,
		CreatedAt:      now.Format(time.RFC3339Nano),
	}

	// O ID real da mensagem é atribuído pelo banco, então a mensagem vai
	// para o pool de persistência e uma goroutine espera a linha gravada.
	// Até o pool responder, a mensagem fica em unsaved para que um sync
	// que chegue nessa janela ainda a encontre.
	var result <-chan models.Message
	if h.persister != nil {
		var err error
		result, err = h.persister.Enqueue(ctx, msg)
		if err != nil {
			// A mensagem não foi aceita, então nada dela pode sobrar: um
			// ACK em cache responderia à nova tentativa do cliente como se
			// ela tivesse sido gravada, e uma sequence consumida deixaria
			// um buraco permanente que os clientes que preenchem lacunas
			// nunca veriam fechado. Run é o único escritor e nada rodou
			// desde nextSequence, então devolver o número é seguro.
			slog.Error("enqueue failed", "conversation_id", msg.ConversationID, "error", err)
			h.seqs[payload.ConversationID] = sequence - 1
			h.sendError(in.client, "internal_error", "could not persist the message", payload.ClientID)
			return
		}
		h.rememberUnsaved(msg)
	}

	// Só agora a mensagem foi realmente aceita: um reenvio do mesmo
	// client_id recebe esta resposta em vez de uma segunda cópia.
	h.rememberAck(key, ack)

	// Entrega a todos os membros, incluindo as outras conexões do
	// remetente, para que uma segunda janela do mesmo usuário fique em dia.
	newFrame, err := encodeFrame(mustEnvelope(ws.TypeMessageNew, msg))
	if err != nil {
		slog.Error("encode message.new failed", "error", err)
		return
	}
	for userID := range members {
		h.deliverToUser(userID, newFrame)
	}

	if result == nil {
		// Sem persistência (testes): responde na hora, sem ID.
		h.sendAck(in.client, ack)
		return
	}

	// Uma goroutine por mensagem cuida do ACK, então o laço do hub nunca
	// espera o disco. Ela não pode mexer no estado do hub; tudo o que
	// descobre volta ao laço por h.persisted.
	go h.awaitPersisted(in.client, key, ack, msg, result)
}

// awaitPersisted envia o ACK quando o pool grava a mensagem, ou depois de
// ackWaitTimeout sem ID, e então informa o resultado ao laço.
//
// Sempre espera a resposta do pool, mesmo depois de um ACK enviado por
// timeout, porque o laço precisa saber quando a mensagem saiu da fila para
// tirá-la de unsaved. O pool responde por toda mensagem, também em caso de
// falha e durante o desligamento, então a espera termina.
func (h *Hub) awaitPersisted(client *Client, key ackKey, ack ws.AckPayload, msg models.Message, result <-chan models.Message) {
	timer := time.NewTimer(ackWaitTimeout)
	defer timer.Stop()

	var row models.Message
	acked := false

	select {
	case row = <-result:
	case <-timer.C:
		slog.Warn("ack sent before the message was stored",
			"conversation_id", msg.ConversationID, "client_id", key.clientID)
		h.sendAck(client, ack)
		acked = true
		row = <-result
	}

	final := ack
	stored := row.ID != 0
	if stored {
		final.ID = row.ID
		final.CreatedAt = row.CreatedAt.Format(time.RFC3339Nano)
	}

	if !acked {
		h.sendAck(client, final)
	}

	select {
	case h.persisted <- persistedMsg{
		key:            key,
		ack:            final,
		stored:         stored,
		conversationID: msg.ConversationID,
		sequence:       msg.Sequence,
	}:
	case <-h.done:
		// O hub parou; não resta estado a atualizar.
	}
}

// sendAck enfileira um frame message.ack para uma conexão.
func (h *Hub) sendAck(client *Client, ack ws.AckPayload) {
	frame, err := encodeFrame(mustEnvelope(ws.TypeMessageAck, ack))
	if err != nil {
		slog.Error("encode message.ack failed", "error", err)
		return
	}
	client.trySend(frame)
}

// rememberUnsaved registra uma mensagem que o banco ainda não confirmou.
// Run é o único chamador.
func (h *Hub) rememberUnsaved(msg models.Message) {
	byConv := h.unsaved[msg.ConversationID]
	if byConv == nil {
		byConv = make(map[int64]models.Message)
		h.unsaved[msg.ConversationID] = byConv
	}
	byConv[msg.Sequence] = msg
}

// handlePersisted roda no laço quando o pool respondeu por uma mensagem: a
// mensagem sai de unsaved, e o cache de deduplicação aprende o ID do banco
// quando houver um.
func (h *Hub) handlePersisted(pm persistedMsg) {
	if byConv := h.unsaved[pm.conversationID]; byConv != nil {
		delete(byConv, pm.sequence)
		if len(byConv) == 0 {
			delete(h.unsaved, pm.conversationID)
		}
	}

	if pm.stored {
		h.rememberAck(pm.key, pm.ack)
	}
}

// ackWaitTimeout limita quanto tempo o ACK espera pelo ID atribuído pelo
// banco.
//
// O pool de persistência despacha a cada 200ms, então isto cobre com folga
// uma gravação normal e ainda garante uma resposta se o banco estiver
// lento ou indisponível: a mensagem é entregue de qualquer forma, e o
// cliente descobre o ID pelo message.new.
const ackWaitTimeout = 2 * time.Second

// nextSequence devolve o próximo número de sequence de uma conversa,
// inicializando o contador em memória a partir do banco no primeiro uso.
func (h *Hub) nextSequence(ctx context.Context, conversationID int64) (int64, error) {
	current, ok := h.seqs[conversationID]
	if !ok {
		if h.store == nil {
			current = 0
		} else {
			stored, err := h.store.LastSequence(ctx, conversationID)
			if err != nil {
				return 0, err
			}
			current = stored
		}
	}

	next := current + 1
	h.seqs[conversationID] = next

	return next, nil
}

// handleReceipt processa uma confirmação de entrega ou de leitura.
//
// O status vai para os outros membros na hora. A gravação da marca é
// entregue ao gravador de confirmações, que a junta em lote com as outras
// confirmações dos milissegundos seguintes.
func (h *Hub) handleReceipt(ctx context.Context, in inbound, status string) {
	var payload ws.ReadReceiptPayload
	if err := json.Unmarshal(in.env.Data, &payload); err != nil {
		h.sendError(in.client, "invalid_payload", "malformed receipt payload", "")
		return
	}
	if payload.UpToSequence <= 0 {
		h.sendError(in.client, "invalid_sequence", "up_to_sequence must be a positive integer", "")
		return
	}

	userID := in.client.UserID()

	members, err := h.conversationMembers(ctx, payload.ConversationID)
	if err != nil {
		h.sendError(in.client, "conversation_not_found", "conversation not found", "")
		return
	}
	if _, isMember := members[userID]; !isMember {
		h.sendError(in.client, "not_a_member", "you are not a member of this conversation", "")
		return
	}

	h.queueReceipt(payload.ConversationID, userID, payload.UpToSequence, status)

	statusFrame, err := encodeFrame(mustEnvelope(ws.TypeMessageStatus, ws.StatusPayload{
		ConversationID: payload.ConversationID,
		UserID:         userID,
		UpToSequence:   payload.UpToSequence,
		Status:         status,
	}))
	if err != nil {
		slog.Error("encode message.status failed", "error", err)
		return
	}

	for memberID := range members {
		if memberID == userID {
			continue
		}
		h.deliverToUser(memberID, statusFrame)
	}
}

// queueReceipt entrega uma confirmação ao gravador de confirmações sem
// bloquear o laço.
//
// Quando a fila está cheia, a gravação é pulada e registrada no log. Nada
// se perde de vez: as marcas são cumulativas, então a próxima confirmação
// do membro carrega esta junto.
func (h *Hub) queueReceipt(conversationID, userID, upTo int64, status string) {
	if h.store == nil {
		return
	}

	r := db.Receipt{ConversationID: conversationID, UserID: userID}
	if status == models.StatusRead {
		r.Read = upTo
	} else {
		r.Delivered = upTo
	}

	select {
	case h.receipts <- r:
	default:
		slog.Warn("receipt queue full, skipping write",
			"conversation_id", conversationID, "user_id", userID, "status", status)
	}
}

// runReceiptWriter junta as confirmações numa entrada por (conversa,
// membro), mantendo as maiores sequences de entrega e de leitura, e as
// grava numa transação a cada receiptFlushEvery ou quando há
// maxReceiptBatch membros pendentes. Retorna depois que a fila é fechada e
// despachada.
func (h *Hub) runReceiptWriter() {
	defer h.wg.Done()

	type memberKey struct{ conversationID, userID int64 }
	pending := make(map[memberKey]db.Receipt)

	flush := func() {
		if len(pending) == 0 {
			return
		}

		batch := make([]db.Receipt, 0, len(pending))
		for _, r := range pending {
			batch = append(batch, r)
		}
		clear(pending)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := h.store.ApplyReceipts(ctx, batch); err != nil {
			slog.Error("persist receipts failed", "count", len(batch), "error", err)
		}
	}

	ticker := time.NewTicker(receiptFlushEvery)
	defer ticker.Stop()

	for {
		select {
		case r, ok := <-h.receipts:
			if !ok {
				flush()
				return
			}

			key := memberKey{r.ConversationID, r.UserID}
			current := pending[key]
			current.ConversationID, current.UserID = r.ConversationID, r.UserID
			current.Delivered = max(current.Delivered, r.Delivered)
			current.Read = max(current.Read, r.Read)
			pending[key] = current

			if len(pending) >= maxReceiptBatch {
				flush()
			}

		case <-ticker.C:
			flush()
		}
	}
}

// maxSyncMessagesPerConversation limita o preenchimento offline. Um
// cliente que ficou fora tempo suficiente para passar disso usa a
// paginação REST, o que impede um frame de carregar um payload sem limite.
const maxSyncMessagesPerConversation = 500

// handleSync envia as mensagens que um cliente reconectando perdeu.
func (h *Hub) handleSync(ctx context.Context, in inbound) {
	var payload ws.SyncPayload
	if err := json.Unmarshal(in.env.Data, &payload); err != nil {
		h.sendError(in.client, "invalid_payload", "malformed sync payload", "")
		return
	}

	userID := in.client.UserID()
	client := in.client

	// Tira uma foto das mensagens que o banco ainda não confirmou. Ela é
	// tirada aqui, no laço, antes da consulta abaixo: uma mensagem gravada
	// depois da foto está nela, e uma gravada antes está no banco, então a
	// combinação não perde nenhuma. Mensagens aceitas depois da foto chegam
	// a esta conexão ao vivo, já que ela está registrada.
	unsaved := make(map[int64][]models.Message, len(h.unsaved))
	for convID, byConv := range h.unsaved {
		for _, msg := range byConv {
			unsaved[convID] = append(unsaved[convID], msg)
		}
	}

	// Montar o preenchimento lê o banco, então roda fora do laço do hub; os
	// frames são enfileirados pelo mesmo canal com buffer.
	go func() {
		syncCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if h.store == nil {
			return
		}

		// Parte do que o cliente informou e depois cobre também as
		// conversas que ele não mencionou (um grupo criado enquanto estava
		// offline, por exemplo).
		known := make(map[int64]int64, len(payload.Conversations))
		for _, c := range payload.Conversations {
			known[c.ConversationID] = c.LastSequence
		}

		conversations, err := h.store.ListUserConversations(syncCtx, userID)
		if err != nil {
			slog.Error("sync: list conversations failed", "user_id", userID, "error", err)
			return
		}

		for _, conv := range conversations {
			from := known[conv.ID]

			stored, err := h.store.ListMessagesAfter(syncCtx, conv.ID, from, maxSyncMessagesPerConversation)
			if err != nil {
				slog.Error("sync: list messages failed", "conversation_id", conv.ID, "error", err)
				continue
			}

			messages := mergeBackfill(stored, unsaved[conv.ID], from, maxSyncMessagesPerConversation)

			for _, msg := range messages {
				frame, err := encodeFrame(mustEnvelope(ws.TypeMessageNew, msg))
				if err != nil {
					continue
				}
				if !client.trySend(frame) {
					// A fila encheu: o cliente está lento demais para
					// receber o preenchimento inteiro. Para de enviar, mas
					// segue adiante para que o sync.end ainda seja enviado
					// abaixo — sem ele o cliente esperaria para sempre,
					// sem saber que o preenchimento terminou.
					slog.Warn("sync: client queue full, truncating backfill",
						"user_id", userID, "conversation_id", conv.ID)
					break
				}
			}
		}

		endFrame, err := encodeFrame(ws.Envelope{Type: ws.TypeSyncEnd})
		if err != nil {
			slog.Error("sync: encode sync.end failed", "error", err)
			return
		}
		if !client.trySend(endFrame) {
			slog.Warn("sync: could not queue sync.end", "user_id", userID)
		}
	}()
}

// mergeBackfill combina as mensagens gravadas depois de uma sequence com
// as não confirmadas, descartando duplicatas (uma mensagem pode estar nas
// duas quando foi gravada entre a foto e a consulta), em ordem crescente
// de sequence e limitado a limit.
func mergeBackfill(stored, unsaved []models.Message, after int64, limit int) []models.Message {
	if len(unsaved) == 0 {
		return stored
	}

	bySequence := make(map[int64]models.Message, len(stored)+len(unsaved))
	for _, msg := range unsaved {
		if msg.Sequence > after {
			bySequence[msg.Sequence] = msg
		}
	}
	// As linhas gravadas prevalecem: elas trazem o ID do banco.
	for _, msg := range stored {
		bySequence[msg.Sequence] = msg
	}

	merged := make([]models.Message, 0, len(bySequence))
	for _, msg := range bySequence {
		merged = append(merged, msg)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Sequence < merged[j].Sequence })

	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

// sendError informa uma requisição rejeitada ao cliente que a fez.
func (h *Hub) sendError(client *Client, code, message, clientID string) {
	frame, err := encodeFrame(mustEnvelope(ws.TypeError, ws.ErrorPayload{
		Code:     code,
		Message:  message,
		ClientID: clientID,
	}))
	if err != nil {
		slog.Error("encode error frame failed", "error", err)
		return
	}
	client.trySend(frame)
}
