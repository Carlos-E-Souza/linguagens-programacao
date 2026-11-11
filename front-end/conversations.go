package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/api"
	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/events"
)

// maxGroupNameChars limita o nome do grupo na interface e aqui.
const maxGroupNameChars = 64

// SearchUsers busca usuários pelo nome ou nome de usuário, sem incluir o próprio usuário. Termo vazio devolve [].
func (a *App) SearchUsers(term string) ([]api.User, error) {
	s := a.current()
	if s == nil {
		return nil, errNoSession
	}
	term = strings.TrimSpace(term)
	if term == "" {
		return []api.User{}, nil
	}
	users, err := a.api.SearchUsers(s.ctx, term)
	if err != nil {
		return nil, friendly(err, nil)
	}
	return nonNil(users), nil
}

// CreateDirectConversation abre a conversa individual com o usuário, criando-a se ainda não existir.
func (a *App) CreateDirectConversation(userID int64) (api.Conversation, error) {
	s := a.current()
	if s == nil {
		return api.Conversation{}, errNoSession
	}
	if userID == s.user.ID {
		return api.Conversation{}, errors.New("Escolha outra pessoa para conversar.")
	}
	conv, err := a.api.CreateConversation(s.ctx, api.ConversationDirect, "", []int64{userID})
	if err != nil {
		return api.Conversation{}, friendly(err, map[int]string{
			http.StatusBadRequest: "Esse usuário não existe mais.",
		})
	}
	return a.storeConversation(s, conv)
}

// CreateGroup cria um grupo com o usuário como administrador.
func (a *App) CreateGroup(name string, memberIDs []int64) (api.Conversation, error) {
	s := a.current()
	if s == nil {
		return api.Conversation{}, errNoSession
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return api.Conversation{}, errors.New("Dê um nome ao grupo.")
	}
	if len([]rune(name)) > maxGroupNameChars {
		return api.Conversation{}, errors.New("O nome do grupo pode ter até 64 caracteres.")
	}
	if len(memberIDs) == 0 {
		return api.Conversation{}, errors.New("Escolha pelo menos uma pessoa para o grupo.")
	}
	conv, err := a.api.CreateConversation(s.ctx, api.ConversationGroup, name, memberIDs)
	if err != nil {
		return api.Conversation{}, friendly(err, map[int]string{
			http.StatusBadRequest: "Não foi possível criar o grupo: confira o nome e as pessoas escolhidas.",
		})
	}
	return a.storeConversation(s, conv)
}

// AddMembers adiciona pessoas a um grupo. A conversa atualizada chega pelo conversation.updated do servidor (ou,
// sem conexão, pela atualização da lista na próxima sincronização). Não há busca extra pelo REST aqui: a resposta
// dela pode sair antes de uma mudança seguinte e sobrescrever no cache um conversation.updated mais novo.
func (a *App) AddMembers(conversationID int64, memberIDs []int64) error {
	s := a.current()
	if s == nil {
		return errNoSession
	}
	if len(memberIDs) == 0 {
		return errors.New("Escolha pelo menos uma pessoa.")
	}
	if err := a.api.AddMembers(s.ctx, conversationID, memberIDs); err != nil {
		return friendly(err, map[int]string{
			http.StatusBadRequest: "Não foi possível adicionar: só grupos aceitam novos membros.",
			http.StatusForbidden:  "Você não participa mais deste grupo.",
			http.StatusNotFound:   "Este grupo não existe mais.",
		})
	}
	return nil
}

// RemoveMember remove um membro do grupo (só administradores) ou, com o próprio id, faz o usuário sair.
func (a *App) RemoveMember(conversationID, userID int64) error {
	s := a.current()
	if s == nil {
		return errNoSession
	}
	if err := a.api.RemoveMember(s.ctx, conversationID, userID); err != nil {
		return friendly(err, map[int]string{
			http.StatusBadRequest: "Só é possível remover membros de grupos.",
			http.StatusForbidden:  "Só administradores podem remover outros membros.",
			http.StatusNotFound:   "Essa pessoa não participa mais do grupo.",
		})
	}
	if userID == s.user.ID {
		a.forgetConversation(s, conversationID)
	}
	return nil
}

// storeConversation grava no cache a conversa devolvida pelo servidor e a devolve como o cache a vê (com não
// lidas e última mensagem, se já existia), avisando a interface.
func (a *App) storeConversation(s *session, conv api.Conversation) (api.Conversation, error) {
	if _, err := s.cache.ApplyConversationUpdate(s.ctx, conv); err != nil {
		log.Printf("salvar conversa criada: %v", err)
		return conv, nil
	}
	saved, ok, err := s.cache.GetConversation(s.ctx, conv.ID)
	if err != nil || !ok {
		return conv, nil
	}
	if a.isCurrent(s) {
		a.emit(events.ConversationUpdated, saved)
	}
	return saved, nil
}

// refreshConversation atualiza a lista pelo REST em segundo plano.
func (a *App) refreshConversation(s *session) {
	cached, err := s.cache.ListConversations(s.ctx)
	if err != nil {
		return
	}
	go a.refreshConversations(s, cached)
}

// forgetConversation apaga a conversa do cache e avisa a interface.
func (a *App) forgetConversation(s *session, conversationID int64) {
	if err := s.cache.DeleteConversation(s.ctx, conversationID); err != nil {
		log.Printf("%v", err)
	}
	if a.isCurrent(s) {
		a.emit(events.ConversationRemoved, events.ConversationRemovedPayload{ConversationID: conversationID})
	}
}

// handleConversationUpdated trata o conversation.updated do servidor: grupo criado, membros adicionados ou
// removidos. Se o usuário não está mais entre os membros, foi removido: a conversa sai do cache e da interface.
func (a *App) handleConversationUpdated(s *session, data json.RawMessage) {
	var payload struct {
		Conversation api.Conversation `json:"conversation"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || payload.Conversation.ID <= 0 {
		log.Printf("conversation.updated inválido: %s", data)
		return
	}
	conv := payload.Conversation
	member := false
	for _, m := range conv.Members {
		if m.UserID == s.user.ID {
			member = true
			break
		}
	}
	if !member {
		a.forgetConversation(s, conv.ID)
		return
	}
	existed, err := s.cache.ApplyConversationUpdate(s.ctx, conv)
	if err != nil {
		log.Printf("%v", err)
		return
	}
	a.emitConversation(s, conv.ID)
	if !existed {
		// A lista REST traz a presença atual dos membros, inclusive quando a
		// conversa ainda não tem mensagens. O evento não inclui essa presença.
		// Ela também traz as não lidas e a última mensagem de grupos antigos.
		a.refreshConversation(s)
	}
}
