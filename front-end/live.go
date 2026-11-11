package main

import (
	"encoding/json"
	"log"

	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/api"
	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/events"
	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/wsclient"
)

// maxGapRounds limita quantas vezes fillGap repete a busca quando continuam chegando mensagens além do trecho.
const maxGapRounds = 3

// MarkAsRead registra localmente que a conversa foi lida até upToSequence e envia message.read. Sem conexão, só o
// cache muda: a interface chama de novo quando a conversa volta a ter não lidas.
func (a *App) MarkAsRead(conversationID, upToSequence int64) error {
	s := a.current()
	if s == nil {
		return errNoSession
	}
	if upToSequence <= 0 {
		return nil
	}
	if err := s.cache.MarkRead(s.ctx, conversationID, s.user.ID, upToSequence); err != nil {
		log.Printf("marcar como lida: %v", err)
	}
	a.send(s, "message.read", map[string]any{"conversation_id": conversationID, "up_to_sequence": upToSequence})
	a.emitConversation(s, conversationID)
	return nil
}

// handleMessageNew trata uma mensagem recebida. Durante a sincronização ela fica com o backfill (Fase 8); fora
// dela é gravada, repassada à interface e confirmada ao servidor com message.delivered.
func (a *App) handleMessageNew(s *session, data json.RawMessage) {
	var msg api.Message
	if err := json.Unmarshal(data, &msg); err != nil || msg.ConversationID <= 0 || msg.Sequence <= 0 || msg.ClientID == "" {
		log.Printf("message.new inválida: %s", data)
		return
	}
	msg.Status = api.StatusSent
	if a.collectSyncMessage(s, msg) {
		return
	}

	if _, ok, err := s.cache.GetConversation(s.ctx, msg.ConversationID); err != nil || !ok {
		// Conversa nova (alguém criou com este usuário): a lista do servidor traz a conversa e a última mensagem.
		if err == nil {
			cached, _ := s.cache.ListConversations(s.ctx)
			go a.refreshConversations(s, cached)
		}
		return
	}
	if _, err := s.cache.SaveReceived(s.ctx, []api.Message{msg}, s.user.ID); err != nil {
		log.Printf("salvar mensagem recebida: %v", err)
		return
	}
	own := msg.SenderID == s.user.ID
	// Sem buraco desde o cursor, a mensagem ao vivo também avança a sincronização. Com buraco (algum quadro se
	// perdeu), o trecho que falta é buscado pelo REST na hora, sem esperar a próxima reconexão.
	if cursor, err := s.cache.SyncCursor(s.ctx, msg.ConversationID); err == nil {
		if msg.Sequence == cursor+1 {
			if err := s.cache.SaveSyncCursor(s.ctx, msg.ConversationID, msg.Sequence); err != nil {
				log.Printf("avançar cursor: %v", err)
			}
		} else if msg.Sequence > cursor+1 {
			go a.fillGap(s, msg.ConversationID)
		}
	}

	if !a.isCurrent(s) {
		return
	}
	if saved, ok, err := s.cache.GetMessage(s.ctx, msg.ClientID); err == nil && ok && saved.SenderID == msg.SenderID {
		msg = saved
	}
	a.emit(events.MessageNew, msg)
	a.emitConversation(s, msg.ConversationID)
	if !own {
		a.sendDelivered(s, msg.ConversationID, msg.Sequence)
	}
}

// fillGap busca pelo REST as mensagens entre o cursor de sincronização e a última sequência conhecida da conversa,
// repetindo enquanto chegarem mensagens novas além do trecho buscado. Uma busca por conversa de cada vez; durante a
// sincronização não faz nada, porque completeSync confere a mesma faixa. Se falhar, a próxima sincronização cobre.
func (a *App) fillGap(s *session, conversationID int64) {
	s.gapMu.Lock()
	if s.gapFilling == nil {
		s.gapFilling = make(map[int64]bool)
	}
	if s.gapFilling[conversationID] {
		s.gapMu.Unlock()
		return
	}
	s.gapFilling[conversationID] = true
	s.gapMu.Unlock()
	defer func() {
		s.gapMu.Lock()
		delete(s.gapFilling, conversationID)
		s.gapMu.Unlock()
	}()

	for attempt := 0; attempt < maxGapRounds && a.isCurrent(s) && s.ctx.Err() == nil; attempt++ {
		s.syncMu.Lock()
		syncing := s.syncActive
		s.syncMu.Unlock()
		if syncing {
			return
		}
		cursor, err := s.cache.SyncCursor(s.ctx, conversationID)
		if err != nil {
			return
		}
		last, err := s.cache.LastSequence(s.ctx, conversationID)
		if err != nil || last <= cursor {
			return
		}
		messages, err := a.fetchMessagesSince(s, conversationID, cursor, last)
		if err != nil {
			if s.ctx.Err() == nil {
				log.Printf("buscar buraco da conversa %d: %v", conversationID, err)
			}
			return
		}
		if _, err := s.cache.SaveReceived(s.ctx, messages, s.user.ID); err != nil {
			log.Printf("salvar buraco da conversa %d: %v", conversationID, err)
			return
		}
		if err := s.cache.SaveSyncCursor(s.ctx, conversationID, last); err != nil {
			log.Printf("avançar cursor da conversa %d: %v", conversationID, err)
			return
		}
		if !a.isCurrent(s) {
			return
		}
		a.emit(events.MessagesSynced, events.MessagesSyncedPayload{ConversationID: conversationID, Messages: messages})
		a.emitConversation(s, conversationID)
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].SenderID != s.user.ID {
				a.sendDelivered(s, conversationID, messages[i].Sequence)
				break
			}
		}
	}
}

// handleMessageStatus aplica a confirmação de entrega ou leitura de um membro. Para as mensagens próprias, a
// interface recebe message:status com a posição que todos os outros membros já alcançaram (em grupos, entregue
// ou lida só quando todos confirmaram). O servidor não envia ao usuário as próprias confirmações.
func (a *App) handleMessageStatus(s *session, data json.RawMessage) {
	var status events.MessageStatusPayload
	if err := json.Unmarshal(data, &status); err != nil || status.ConversationID <= 0 ||
		(status.Status != api.StatusDelivered && status.Status != api.StatusRead) {
		log.Printf("message.status inválido: %s", data)
		return
	}
	receipts, err := s.cache.ApplyReceipt(s.ctx, status.ConversationID, status.UserID, status.UpToSequence, status.Status)
	if err != nil {
		log.Printf("aplicar confirmação: %v", err)
		return
	}
	if !a.isCurrent(s) {
		return
	}
	for _, step := range []struct {
		status string
		upTo   int64
	}{{api.StatusDelivered, receipts.Delivered}, {api.StatusRead, receipts.Read}} {
		if step.upTo > 0 {
			a.emit(events.MessageStatus, events.MessageStatusPayload{ConversationID: status.ConversationID,
				UserID: status.UserID, UpToSequence: step.upTo, Status: step.status})
		}
	}
	a.emitConversation(s, status.ConversationID)
}

// handlePresence atualiza o indicador online do contato no cache e na interface.
func (a *App) handlePresence(s *session, data json.RawMessage) {
	var presence events.PresencePayload
	if err := json.Unmarshal(data, &presence); err != nil || presence.UserID <= 0 {
		log.Printf("presence inválido: %s", data)
		return
	}
	if err := s.cache.SetPresence(s.ctx, presence.UserID, presence.Online); err != nil {
		log.Printf("%v", err)
	}
	if a.isCurrent(s) {
		a.emit(events.Presence, presence)
	}
}

// sendDelivered confirma ao servidor o recebimento até a sequência informada.
func (a *App) sendDelivered(s *session, conversationID, upTo int64) {
	a.send(s, "message.delivered", map[string]any{"conversation_id": conversationID, "up_to_sequence": upTo})
}

// send envia um envelope sem garantia de entrega: confirmações perdidas numa queda não voltam para a fila (a
// leitura é reenviada pela interface e a entrega pela sincronização seguinte).
func (a *App) send(s *session, kind string, data any) {
	if s.ws == nil {
		return
	}
	env, err := wsclient.NewEnvelope(kind, data)
	if err == nil {
		err = s.ws.Send(env)
	}
	if err != nil && err != wsclient.ErrNotConnected {
		log.Printf("enviar %s: %v", kind, err)
	}
}

// emitConversation manda a conversa do cache (membros, prévia e não lidas) para a interface.
func (a *App) emitConversation(s *session, conversationID int64) {
	if !a.isCurrent(s) {
		return
	}
	if conv, ok, err := s.cache.GetConversation(s.ctx, conversationID); err == nil && ok {
		a.emit(events.ConversationUpdated, conv)
	}
}
