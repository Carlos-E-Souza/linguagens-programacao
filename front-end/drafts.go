package main

import (
	"errors"
	"log"
)

// LoadDraft lê o rascunho local da conversa para a conta que abriu a tela.
func (a *App) LoadDraft(userID, conversationID int64) (string, error) {
	s := a.current()
	if s == nil || s.user.ID != userID {
		return "", errNoSession
	}
	body, err := s.cache.LoadDraft(s.ctx, conversationID)
	if err != nil {
		log.Printf("ler rascunho: %v", err)
		return "", errors.New("Não foi possível ler o rascunho salvo.")
	}
	return body, nil
}

// SaveDraft grava o texto no cache local; texto vazio remove o rascunho.
func (a *App) SaveDraft(userID, conversationID int64, body string) error {
	s := a.current()
	if s == nil || s.user.ID != userID {
		return errNoSession
	}
	if err := s.cache.SaveDraft(s.ctx, conversationID, body); err != nil {
		log.Printf("salvar rascunho: %v", err)
		return errors.New("Não foi possível salvar o rascunho.")
	}
	return nil
}
