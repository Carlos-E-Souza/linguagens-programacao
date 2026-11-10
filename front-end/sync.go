package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"

	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/api"
	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/events"
	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/wsclient"
)

// startSync é chamada antes de iniciar a leitura da nova conexão. Assim o cursor é
// fotografado antes de qualquer mensagem nova chegar por ela.
func (a *App) startSync(s *session) {
	conversations, err := s.cache.ListConversations(s.ctx)
	if err != nil {
		log.Printf("listar conversas para sync: %v", err)
		go s.queue.ResendAll(s.ctx)
		return
	}
	type position struct {
		ConversationID int64 `json:"conversation_id"`
		LastSequence   int64 `json:"last_sequence"`
	}
	positions := make([]position, 0, len(conversations))
	from := make(map[int64]int64, len(conversations))
	for _, conv := range conversations {
		sequence, err := s.cache.SyncCursor(s.ctx, conv.ID)
		if err != nil {
			log.Printf("ler cursor da conversa %d: %v", conv.ID, err)
			go s.queue.ResendAll(s.ctx)
			return
		}
		from[conv.ID] = sequence
		positions = append(positions, position{ConversationID: conv.ID, LastSequence: sequence})
	}

	s.syncMu.Lock()
	s.syncGeneration++
	s.syncActive = true
	s.syncFrom = from
	s.syncMessages = nil
	s.syncMu.Unlock()

	env, err := wsclient.NewEnvelope("sync", map[string]any{"conversations": positions})
	if err == nil {
		err = s.ws.Send(env)
	}
	if err != nil {
		log.Printf("pedir sync: %v", err)
		s.syncMu.Lock()
		s.syncActive = false
		s.syncMu.Unlock()
		go s.queue.ResendAll(s.ctx)
	}
}

// collectSyncMessage guarda os quadros do backfill até sync.end. O tratamento de
// mensagens ao vivo pertence à Fase 7, desenvolvida em outra branch.
func (a *App) collectSyncMessage(s *session, data json.RawMessage) {
	var msg api.Message
	if err := json.Unmarshal(data, &msg); err != nil || msg.ConversationID <= 0 || msg.Sequence <= 0 || msg.ClientID == "" {
		log.Printf("message.new de sync inválida: %s", data)
		return
	}
	msg.Status = api.StatusSent
	s.syncMu.Lock()
	if s.syncActive {
		s.syncMessages = append(s.syncMessages, msg)
	}
	s.syncMu.Unlock()
}

func (a *App) finishSync(s *session) {
	s.syncMu.Lock()
	if !s.syncActive {
		s.syncMu.Unlock()
		return
	}
	generation := s.syncGeneration
	from := s.syncFrom
	received := s.syncMessages
	s.syncActive = false
	s.syncMessages = nil
	s.syncMu.Unlock()
	go a.completeSync(s, generation, from, received)
}

func (a *App) syncIsCurrent(s *session, generation uint64) bool {
	s.syncMu.Lock()
	current := s.syncGeneration == generation
	s.syncMu.Unlock()
	return current && a.isCurrent(s) && s.ctx.Err() == nil
}

// completeSync confere via REST o intervalo que o WebSocket recuperou. O servidor
// limita o backfill a 500 mensagens por conversa, então REST completa o restante.
func (a *App) completeSync(s *session, generation uint64, from map[int64]int64, received []api.Message) {
	defer func() {
		if a.syncIsCurrent(s, generation) {
			s.queue.ResendAll(s.ctx)
		}
	}()

	fresh, err := a.api.ListConversations(s.ctx)
	if !a.syncIsCurrent(s, generation) {
		return
	}
	canAdvance := err == nil
	if err != nil {
		log.Printf("atualizar conversas após sync: %v", err)
		fresh, err = s.cache.ListConversations(s.ctx)
		if err != nil {
			return
		}
	} else {
		removed, err := s.cache.ReplaceConversations(s.ctx, fresh)
		if err != nil {
			log.Printf("salvar conversas após sync: %v", err)
			return
		}
		if a.syncIsCurrent(s, generation) {
			for _, id := range removed {
				a.emit(events.ConversationRemoved, events.ConversationRemovedPayload{ConversationID: id})
			}
		}
	}

	buffered := make(map[int64][]api.Message)
	for _, msg := range received {
		buffered[msg.ConversationID] = append(buffered[msg.ConversationID], msg)
	}
	for _, conv := range fresh {
		if !a.syncIsCurrent(s, generation) {
			return
		}
		cursor := from[conv.ID] // conversa criada enquanto estava offline começa em zero
		messages := buffered[conv.ID]
		complete := false
		if canAdvance {
			history, err := a.fetchMessagesSince(s, conv.ID, cursor, conv.LastSequence)
			if err != nil {
				log.Printf("recuperar conversa %d: %v", conv.ID, err)
			} else {
				messages = append(messages, history...)
				complete = true
			}
		}
		if !a.syncIsCurrent(s, generation) {
			return
		}
		messages = uniqueNewMessages(messages, cursor)
		if len(messages) > 0 {
			if err := s.cache.SaveMessages(s.ctx, messages); err != nil {
				log.Printf("salvar mensagens sincronizadas da conversa %d: %v", conv.ID, err)
				continue
			}
			if a.syncIsCurrent(s, generation) {
				a.emit(events.MessagesSynced, events.MessagesSyncedPayload{ConversationID: conv.ID, Messages: messages})
			}
		}
		if complete {
			if err := s.cache.SaveSyncCursor(s.ctx, conv.ID, conv.LastSequence); err != nil {
				log.Printf("avançar cursor da conversa %d: %v", conv.ID, err)
			}
		}
		if a.syncIsCurrent(s, generation) {
			if saved, ok, err := s.cache.GetConversation(s.ctx, conv.ID); err == nil && ok {
				a.emit(events.ConversationUpdated, saved)
			}
		}
	}
}

// fetchMessagesSince pagina para trás até alcançar o cursor antigo. Conferir
// toda a faixa também cobre buracos se algum quadro WebSocket foi perdido.
func (a *App) fetchMessagesSince(s *session, conversationID, from, to int64) ([]api.Message, error) {
	if to <= from {
		return nil, nil
	}
	before := int64(0)
	bySequence := make(map[int64]api.Message)
	for {
		page, err := a.api.ListMessages(s.ctx, conversationID, before, pageSize)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		oldest := page[0].Sequence
		if oldest <= 0 || (before > 0 && oldest >= before) {
			return nil, errors.New("paginação do histórico não avançou")
		}
		for _, msg := range page {
			if msg.Sequence > from && msg.Sequence <= to {
				msg.Status = api.StatusSent
				bySequence[msg.Sequence] = msg
			}
		}
		if oldest <= from+1 || len(page) < pageSize {
			break
		}
		before = oldest
	}
	messages := make([]api.Message, 0, len(bySequence))
	for _, msg := range bySequence {
		messages = append(messages, msg)
	}
	sort.Slice(messages, func(i, j int) bool { return messages[i].Sequence < messages[j].Sequence })
	expected := from + 1
	for _, msg := range messages {
		if msg.Sequence != expected {
			return nil, fmt.Errorf("faltou sequência %d", expected)
		}
		expected++
	}
	if expected-1 != to {
		return nil, fmt.Errorf("faltou sequência %d", expected)
	}
	return messages, nil
}

func uniqueNewMessages(messages []api.Message, after int64) []api.Message {
	bySequence := make(map[int64]api.Message)
	for _, msg := range messages {
		if msg.Sequence > after {
			msg.Status = api.StatusSent
			bySequence[msg.Sequence] = msg
		}
	}
	result := make([]api.Message, 0, len(bySequence))
	for _, msg := range bySequence {
		result = append(result, msg)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Sequence < result[j].Sequence })
	return result
}
