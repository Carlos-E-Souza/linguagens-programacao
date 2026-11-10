package httputil

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/db"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
)

// userResult is a user as returned by search, with the presence flag the
// client needs to render an online indicator.
type userResult struct {
	models.User
	Online bool `json:"online"`
}

// handleSearchUsers answers GET /api/users?search=<term>.
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

// handleListConversations answers GET /api/conversations.
//
// Members are fetched per conversation so the client can render groups
// without a second round trip. That is N+1 queries, which is acceptable
// here because a user belongs to a bounded number of conversations; if
// that changes, this is the first place to batch.
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

// createConversationRequest is the body of POST /api/conversations.
type createConversationRequest struct {
	Type    string  `json:"type"`
	Name    string  `json:"name"`
	Members []int64 `json:"members"`
}

// handleCreateConversation answers POST /api/conversations.
//
// A direct conversation is idempotent: asking twice returns the existing
// one with 200 instead of creating a duplicate, which is what makes a
// double click harmless.
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

// createDirectConversation handles a 1:1 conversation request.
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

	// Reuse the existing conversation when there is one.
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

	s.respondWithConversation(w, r, conv, http.StatusCreated)
}

// createGroupConversation handles a group creation request.
func (s *Server) createGroupConversation(w http.ResponseWriter, r *http.Request, userID int64, req createConversationRequest) {
	name := req.Name
	if name == "" {
		WriteError(w, http.StatusBadRequest, "invalid_name", "a group requires a name")
		return
	}

	// The creator is always a member and always the admin, regardless of
	// what the request body says.
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

	s.respondWithConversation(w, r, conv, http.StatusCreated)
}

// respondWithConversation loads the members and writes the conversation.
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

// addMembersRequest is the body of POST /api/conversations/{id}/members.
type addMembersRequest struct {
	Members []int64 `json:"members"`
}

// handleAddMembers answers POST /api/conversations/{id}/members.
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

	s.respondWithConversation(w, r, conv, http.StatusOK)
}

// handleRemoveMember answers DELETE /api/conversations/{id}/members/{userId}.
//
// A user may always remove themselves; removing somebody else requires
// the admin role.
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

	WriteJSON(w, http.StatusOK, map[string]any{"conversation_id": conv.ID, "user_id": targetID})
}

// handleListMessages answers GET /api/conversations/{id}/messages.
//
// The response is always in ascending sequence order, which is the order
// a chat view renders, regardless of the DESC walk the query performs.
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

// requireMembership resolves the {id} path value and verifies the caller
// belongs to that conversation.
//
// Both failures write their own response, so callers only need to check
// the boolean. Answering 404 instead of 403 for a non-member avoids
// revealing that the conversation exists.
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

// queryInt reads an optional integer query parameter.
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

// queryInt64 reads an optional 64-bit integer query parameter.
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
