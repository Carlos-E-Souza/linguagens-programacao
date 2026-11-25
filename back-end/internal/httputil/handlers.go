package httputil

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/db"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/hub"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
)

// userResult é um usuário como devolvido pela busca, com o indicador de
// presença de que o cliente precisa para mostrar quem está online.
type userResult struct {
	models.User
	Online bool `json:"online"`
}

// handleSearchUsers responde GET /api/users?search=<termo>.
func (s *Server) handleSearchUsers(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.currentUser(w, r)
	if !ok {
		return
	}

	term := r.URL.Query().Get("search")

	users, err := s.store.SearchUsers(r.Context(), term, userID, maxSearchResults)
	if err != nil {
		logRequestError(r, "search_users", err)
		WriteError(w, http.StatusInternalServerError, "internal_error", "could not search users")
		return
	}

	results := make([]userResult, 0, len(users))
	for _, user := range users {
		results = append(results, userResult{User: user, Online: s.isOnline(user.ID)})
	}

	WriteJSON(w, http.StatusOK, results)
}

// handleListConversations responde GET /api/conversations.
//
// Os membros são buscados por conversa para que o cliente mostre os
// grupos sem uma segunda ida ao servidor. São N+1 consultas, o que é
// aceitável aqui porque um usuário participa de um número limitado de
// conversas; se isso mudar, este é o primeiro lugar a agrupar em lote.
func (s *Server) handleListConversations(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.currentUser(w, r)
	if !ok {
		return
	}

	conversations, err := s.store.ListUserConversations(r.Context(), userID)
	if err != nil {
		logRequestError(r, "list_conversations", err)
		WriteError(w, http.StatusInternalServerError, "internal_error", "could not list conversations")
		return
	}

	for i := range conversations {
		members, err := s.store.ListMembers(r.Context(), conversations[i].ID)
		if err != nil {
			logRequestError(r, "list_members", err)
			WriteError(w, http.StatusInternalServerError, "internal_error", "could not list members")
			return
		}
		for j := range members {
			members[j].Online = s.isOnline(members[j].UserID)
		}
		conversations[i].Members = members
	}

	WriteJSON(w, http.StatusOK, conversations)
}

// createConversationRequest é o corpo de POST /api/conversations.
type createConversationRequest struct {
	Type    string  `json:"type"`
	Name    string  `json:"name"`
	Members []int64 `json:"members"`
}

// handleCreateConversation responde POST /api/conversations.
//
// Uma conversa individual é idempotente: pedir duas vezes devolve a
// existente com 200 em vez de criar uma duplicata, o que torna um clique
// duplo inofensivo.
func (s *Server) handleCreateConversation(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.currentUser(w, r)
	if !ok {
		return
	}

	var req createConversationRequest
	if err := ReadJSON(w, r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}

	switch req.Type {
	case models.ConversationDirect:
		s.createDirectConversation(w, r, userID, req)
	case models.ConversationGroup:
		s.createGroupConversation(w, r, userID, req)
	default:
		WriteError(w, http.StatusBadRequest, "invalid_type",
			`type must be "direct" or "group"`)
	}
}

// createDirectConversation trata o pedido de uma conversa 1:1.
func (s *Server) createDirectConversation(w http.ResponseWriter, r *http.Request, userID int64, req createConversationRequest) {
	if len(req.Members) != 1 {
		WriteError(w, http.StatusBadRequest, "invalid_members",
			"a direct conversation requires exactly one other member")
		return
	}

	otherID := req.Members[0]
	if otherID == userID {
		WriteError(w, http.StatusBadRequest, "invalid_members",
			"cannot start a direct conversation with yourself")
		return
	}

	// Reaproveita a conversa existente quando houver uma.
	existing, err := s.store.GetDirectConversation(r.Context(), userID, otherID)
	if err == nil {
		s.respondWithConversation(w, r, existing, http.StatusOK)
		return
	}
	if !errors.Is(err, db.ErrNotFound) {
		logRequestError(r, "get_direct_conversation", err)
		WriteError(w, http.StatusInternalServerError, "internal_error", "could not look up the conversation")
		return
	}

	conv, err := s.store.CreateConversation(r.Context(),
		models.ConversationDirect, "", userID, []int64{userID, otherID}, nil)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			WriteError(w, http.StatusBadRequest, "unknown_member", "that user does not exist")
			return
		}
		logRequestError(r, "create_conversation", err)
		WriteError(w, http.StatusInternalServerError, "internal_error", "could not create the conversation")
		return
	}

	slog.Info("direct conversation created", "conversation_id", conv.ID, "created_by", userID)

	s.notifyConversation(r, conv.ID)

	s.respondWithConversation(w, r, conv, http.StatusCreated)
}

// createGroupConversation trata o pedido de criação de um grupo.
func (s *Server) createGroupConversation(w http.ResponseWriter, r *http.Request, userID int64, req createConversationRequest) {
	name := req.Name
	if name == "" {
		WriteError(w, http.StatusBadRequest, "invalid_name", "a group requires a name")
		return
	}

	// Quem cria é sempre membro e sempre admin, independentemente do que
	// diz o corpo da requisição.
	memberIDs := []int64{userID}
	seen := map[int64]bool{userID: true}
	for _, id := range req.Members {
		if seen[id] {
			continue
		}
		seen[id] = true
		memberIDs = append(memberIDs, id)
	}

	roles := map[int64]string{userID: models.RoleAdmin}

	conv, err := s.store.CreateConversation(r.Context(),
		models.ConversationGroup, name, userID, memberIDs, roles)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			WriteError(w, http.StatusBadRequest, "unknown_member", "one of those users does not exist")
			return
		}
		logRequestError(r, "create_group", err)
		WriteError(w, http.StatusInternalServerError, "internal_error", "could not create the group")
		return
	}

	slog.Info("group created", "conversation_id", conv.ID, "created_by", userID, "members", len(memberIDs))

	s.notifyConversation(r, conv.ID)

	s.respondWithConversation(w, r, conv, http.StatusCreated)
}

// respondWithConversation carrega os membros e escreve a conversa.
func (s *Server) respondWithConversation(w http.ResponseWriter, r *http.Request, conv models.Conversation, status int) {
	members, err := s.store.ListMembers(r.Context(), conv.ID)
	if err != nil {
		logRequestError(r, "list_members", err)
		WriteError(w, http.StatusInternalServerError, "internal_error", "could not list members")
		return
	}
	for i := range members {
		members[i].Online = s.isOnline(members[i].UserID)
	}
	conv.Members = members

	WriteJSON(w, status, conv)
}

// addMembersRequest é o corpo de POST /api/conversations/{id}/members.
type addMembersRequest struct {
	Members []int64 `json:"members"`
}

// handleAddMembers responde POST /api/conversations/{id}/members.
func (s *Server) handleAddMembers(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.currentUser(w, r)
	if !ok {
		return
	}

	conv, ok := s.requireMembership(w, r, userID)
	if !ok {
		return
	}

	if conv.Type != models.ConversationGroup {
		WriteError(w, http.StatusBadRequest, "not_a_group", "members can only be added to a group")
		return
	}

	var req addMembersRequest
	if err := ReadJSON(w, r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if len(req.Members) == 0 {
		WriteError(w, http.StatusBadRequest, "invalid_members", "at least one member is required")
		return
	}

	if err := s.store.AddMembers(r.Context(), conv.ID, req.Members); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			WriteError(w, http.StatusBadRequest, "unknown_member", "one of those users does not exist")
			return
		}
		logRequestError(r, "add_members", err)
		WriteError(w, http.StatusInternalServerError, "internal_error", "could not add members")
		return
	}

	s.notifyConversation(r, conv.ID)

	s.respondWithConversation(w, r, conv, http.StatusOK)
}

// handleRemoveMember responde DELETE /api/conversations/{id}/members/{userId}.
//
// Um usuário sempre pode remover a si mesmo; remover outra pessoa exige o
// papel de admin. Só grupos têm membros removíveis: sair de uma conversa
// individual a deixaria órfã, e o próximo POST /api/conversations para o
// mesmo par criaria uma segunda.
func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.currentUser(w, r)
	if !ok {
		return
	}

	targetID, err := strconv.ParseInt(r.PathValue("userId"), 10, 64)
	if err != nil || targetID <= 0 {
		WriteError(w, http.StatusBadRequest, "invalid_user_id", "userId must be a positive integer")
		return
	}

	conv, ok := s.requireMembership(w, r, userID)
	if !ok {
		return
	}

	if conv.Type != models.ConversationGroup {
		WriteError(w, http.StatusBadRequest, "not_a_group", "members can only be removed from a group")
		return
	}

	if targetID != userID {
		role, err := s.store.GetMemberRole(r.Context(), conv.ID, userID)
		if err != nil {
			logRequestError(r, "get_member_role", err)
			WriteError(w, http.StatusInternalServerError, "internal_error", "could not verify permissions")
			return
		}
		if role != models.RoleAdmin {
			WriteError(w, http.StatusForbidden, "not_admin", "only an admin can remove other members")
			return
		}
	}

	if err := s.store.RemoveMember(r.Context(), conv.ID, targetID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "not_a_member", "that user is not a member of this conversation")
			return
		}
		logRequestError(r, "remove_member", err)
		WriteError(w, http.StatusInternalServerError, "internal_error", "could not remove the member")
		return
	}

	slog.Info("member removed", "conversation_id", conv.ID, "user_id", targetID, "by", userID)

	// O usuário removido não é mais membro, então é indicado explicitamente:
	// sem isso ele nunca saberia que a conversa sumiu para ele.
	s.notifyConversation(r, conv.ID, targetID)

	WriteJSON(w, http.StatusOK, map[string]any{"conversation_id": conv.ID, "user_id": targetID})
}

// handleListMessages responde GET /api/conversations/{id}/messages.
//
// A resposta vem sempre em ordem crescente de sequence, que é a ordem em
// que a tela de chat desenha, independentemente da leitura DESC feita
// pela consulta.
func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.currentUser(w, r)
	if !ok {
		return
	}

	conv, ok := s.requireMembership(w, r, userID)
	if !ok {
		return
	}

	before, err := queryInt64(r, "before", 0)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_before", "before must be a non-negative integer")
		return
	}

	limit, err := queryInt(r, "limit", defaultMessagePage)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_limit", "limit must be a positive integer")
		return
	}
	if limit > maxMessagePage {
		limit = maxMessagePage
	}
	if limit < 1 {
		limit = defaultMessagePage
	}

	messages, err := s.store.ListMessages(r.Context(), conv.ID, before, limit)
	if err != nil {
		logRequestError(r, "list_messages", err)
		WriteError(w, http.StatusInternalServerError, "internal_error", "could not load messages")
		return
	}

	WriteJSON(w, http.StatusOK, messages)
}

// requireMembership resolve o valor {id} do caminho e confere que quem
// chama participa daquela conversa.
//
// As duas falhas escrevem a própria resposta, então quem chama só precisa
// checar o booleano. Responder 404 em vez de 403 para quem não é membro
// evita revelar que a conversa existe.
func (s *Server) requireMembership(w http.ResponseWriter, r *http.Request, userID int64) (models.Conversation, bool) {
	convID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || convID <= 0 {
		WriteError(w, http.StatusBadRequest, "invalid_id", "id must be a positive integer")
		return models.Conversation{}, false
	}

	conv, err := s.store.GetConversation(r.Context(), convID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "not_found", "conversation not found")
			return models.Conversation{}, false
		}
		logRequestError(r, "get_conversation", err)
		WriteError(w, http.StatusInternalServerError, "internal_error", "could not load the conversation")
		return models.Conversation{}, false
	}

	isMember, err := s.store.IsMember(r.Context(), convID, userID)
	if err != nil {
		logRequestError(r, "is_member", err)
		WriteError(w, http.StatusInternalServerError, "internal_error", "could not verify membership")
		return models.Conversation{}, false
	}
	if !isMember {
		WriteError(w, http.StatusNotFound, "not_found", "conversation not found")
		return models.Conversation{}, false
	}

	return conv, true
}

// queryInt lê um parâmetro de query inteiro opcional.
func queryInt(r *http.Request, name string, fallback int) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, errors.New("invalid integer query parameter")
	}
	return value, nil
}

// queryInt64 lê um parâmetro de query inteiro de 64 bits opcional.
func queryInt64(r *http.Request, name string, fallback int64) (int64, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, errors.New("invalid integer query parameter")
	}
	return value, nil
}

// notifyConversation avisa o hub que a participação numa conversa mudou,
// para que os membros conectados atualizem a lista de membros.
//
// É melhor esforço de propósito: a chamada REST já deu certo e seu
// resultado é válido haja ou não algum cliente conectado, então a falta
// do hub não pode transformar a requisição em erro.
//
// formerMembers são usuários que também precisam receber a atualização
// embora não sejam mais membros, como quem acabou de ser removido.
func (s *Server) notifyConversation(r *http.Request, conversationID int64, formerMembers ...int64) {
	if s.hub == nil {
		return
	}

	s.hub.Notify(r.Context(), hub.Notification{
		Type:           hub.NotifyConversationUpdated,
		ConversationID: conversationID,
		UserIDs:        formerMembers,
	})
}
