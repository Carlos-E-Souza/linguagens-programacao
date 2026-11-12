package main

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/api"
	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/events"
	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/wsclient"
)

// syncTimeout é o prazo para o sync.end chegar depois do pedido de sincronização.
var syncTimeout = 15 * time.Second

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
	generation := s.syncGeneration
	s.syncActive = true
	s.syncFrom = from
	s.syncMessages = nil
	s.syncMu.Unlock()
	// O servidor pode não mandar sync.end (fila cheia, falha ao ler o banco). Sem prazo, as mensagens ao vivo
	// ficariam presas no buffer até a próxima reconexão. completeSync confere tudo pelo REST, então encerrar antes
	// do fim do backfill não perde mensagens: o que chegar depois é tratado como mensagem ao vivo.
	time.AfterFunc(syncTimeout, func() { a.finishSyncGeneration(s, generation) })

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

// collectSyncMessage guarda os quadros do backfill até sync.end. Devolve false fora da
// sincronização: a mensagem é ao vivo e segue para handleMessageNew.
func (a *App) collectSyncMessage(s *session, msg api.Message) bool {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	if s.syncActive {
		s.syncMessages = append(s.syncMessages, msg)
	}
	return s.syncActive
}

// finishSync trata o sync.end da sincronização em andamento.
func (a *App) finishSync(s *session) {
	s.syncMu.Lock()
	generation := s.syncGeneration
	s.syncMu.Unlock()
	a.finishSyncGeneration(s, generation)
}

// finishSyncGeneration encerra a sincronização indicada, se ela ainda estiver ativa: pelo sync.end ou pelo prazo.
func (a *App) finishSyncGeneration(s *session, generation uint64) {
	s.syncMu.Lock()
	if !s.syncActive || s.syncGeneration != generation || s.ctx.Err() != nil {
		s.syncMu.Unlock()
		return
	}
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
	fresh, removed, err := a.fetchConversations(s)
	if !a.syncIsCurrent(s, generation) {
		return
	}
	if errors.Is(err, errStaleConversations) {
		// O cache mudou durante três buscas. Tentar novamente mais tarde
		// mantém o contador de não lidas e o cursor até a lista REST estar
		// estável, sem bloquear a leitura de novos eventos WebSocket.
		go func() {
			timer := time.NewTimer(200 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-s.ctx.Done():
				return
			case <-timer.C:
				if a.syncIsCurrent(s, generation) {
					a.completeSync(s, generation, from, received)
				}
			}
		}()
		return
	}
	defer func() {
		if a.syncIsCurrent(s, generation) {
			s.queue.ResendAll(s.ctx)
		}
	}()
	canAdvance := err == nil
	if err != nil {
		log.Printf("atualizar conversas após sync: %v", err)
		fresh, err = s.cache.ListConversations(s.ctx)
		if err != nil {
			return
		}
	} else if a.syncIsCurrent(s, generation) {
		for _, id := range removed {
			a.emit(events.ConversationRemoved, events.ConversationRemovedPayload{ConversationID: id})
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
			// Confirma ao servidor o recebimento do que chegou enquanto estava offline.
			for i := len(messages) - 1; i >= 0; i-- {
				if messages[i].SenderID != s.user.ID {
					a.sendDelivered(s, conv.ID, messages[i].Sequence)
					break
				}
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
