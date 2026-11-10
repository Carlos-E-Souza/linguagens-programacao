package httputil

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/coder/websocket"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/auth"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/config"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/db"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/hub"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/ws"
)

// maxSearchResults caps how many users a search returns. Without a limit
// a broad term such as "a" would serialise the whole users table.
const maxSearchResults = 20

// defaultMessagePage and maxMessagePage bound the message history
// pagination: the default keeps a first load small, the maximum stops a
// client from asking for the entire conversation at once.
const (
	defaultMessagePage = 50
	maxMessagePage     = 100
)

// Presence reports which users currently have an open connection. The
// hub implements it; keeping it an interface here means the HTTP layer
// does not depend on the hub package.
type Presence interface {
	IsOnline(userID int64) bool
}

// TokenValidator validates a raw bearer token and returns the user ID.
// It mirrors middleware.TokenValidator; the HTTP layer declares its own
// copy so that httputil and middleware do not import each other.
type TokenValidator interface {
	ValidateToken(raw string) (int64, error)
}

// Server holds the dependencies shared by every HTTP handler.
//
// Handlers are methods on this type rather than free functions so the
// dependencies travel with them. There is no global state, which is what
// makes each handler testable in isolation.
type Server struct {
	cfg       config.Config
	store     *db.Store
	hasher    *auth.Hasher
	issuer    *auth.TokenIssuer
	validator TokenValidator
	presence  Presence
	hub       RealtimeHub
}

// RealtimeHub is the part of the hub the HTTP layer needs: opening
// connections, registering them and pushing REST-driven notifications.
type RealtimeHub interface {
	NewClient(userID int64, conn *websocket.Conn) *hub.Client
	Register(client *hub.Client)
	Unregister(client *hub.Client)
	Submit(ctx context.Context, client *hub.Client, env ws.Envelope, rawSize int) error
	IsOnline(userID int64) bool
	Notify(ctx context.Context, n hub.Notification)
}

// SetHub attaches the realtime hub. It is set after construction because
// the hub itself needs the store, and because tests that do not exercise
// the WebSocket path can leave it nil.
func (s *Server) SetHub(h RealtimeHub) {
	s.hub = h
	s.presence = h
}

// NewServer builds the HTTP layer.
func NewServer(cfg config.Config, store *db.Store, hasher *auth.Hasher, issuer *auth.TokenIssuer) *Server {
	return &Server{
		cfg:       cfg,
		store:     store,
		hasher:    hasher,
		issuer:    issuer,
		validator: issuer,
	}
}

// SetPresence attaches the presence source used to annotate users and
// members with their online state.
func (s *Server) SetPresence(p Presence) {
	s.presence = p
}

// isOnline reports the online state of a user, defaulting to false when
// no presence source is wired.
func (s *Server) isOnline(userID int64) bool {
	if s.presence == nil {
		return false
	}
	return s.presence.IsOnline(userID)
}

// Routes builds the router with every endpoint registered.
//
// Go 1.22+ patterns carry the method and the wildcards, so the mux
// itself rejects a wrong method with 405 and the handler reads the
// wildcard with r.PathValue. No third-party router is needed.
//
// Authentication is applied here as a wrapper around the protected
// subtree rather than inside each handler, so forgetting to protect a new
// route is a deliberate act instead of an oversight.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// Public.
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /ws", s.handleWS)
	mux.HandleFunc("POST /api/auth/register", s.handleRegister)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)

	// Protected: everything below requires a valid bearer token.
	protected := http.NewServeMux()
	protected.HandleFunc("GET /api/users", s.handleSearchUsers)
	protected.HandleFunc("GET /api/conversations", s.handleListConversations)
	protected.HandleFunc("POST /api/conversations", s.handleCreateConversation)
	protected.HandleFunc("POST /api/conversations/{id}/members", s.handleAddMembers)
	protected.HandleFunc("DELETE /api/conversations/{id}/members/{userId}", s.handleRemoveMember)
	protected.HandleFunc("GET /api/conversations/{id}/messages", s.handleListMessages)

	mux.Handle("/api/users", s.authenticate(protected))
	mux.Handle("/api/users/", s.authenticate(protected))
	mux.Handle("/api/conversations", s.authenticate(protected))
	mux.Handle("/api/conversations/", s.authenticate(protected))

	return mux
}

// contextKey is an unexported type for the user ID context key, so it
// cannot collide with a key set by another package.
type contextKey struct{}

// userIDKey is where the authenticated user ID is stored.
var userIDKey = contextKey{}

// authenticate rejects requests without a valid bearer token and stores
// the user ID in the request context.
//
// This duplicates middleware.Authenticate by necessity: middleware
// imports httputil for its error responses, so httputil cannot import
// middleware back without a cycle. The HTTP layer owns its own copy and
// UserFromContext reads it.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.validator == nil {
			WriteError(w, http.StatusServiceUnavailable,
				"not_configured", "authentication is not configured")
			return
		}

		raw, err := auth.BearerToken(r.Header.Get("Authorization"))
		if err != nil {
			WriteError(w, http.StatusUnauthorized,
				"unauthorized", "missing or malformed authorization header")
			return
		}

		userID, err := s.validator.ValidateToken(raw)
		if err != nil {
			WriteError(w, http.StatusUnauthorized,
				"unauthorized", "invalid or expired token")
			return
		}

		ctx := context.WithValue(r.Context(), userIDKey, userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UserFromContext returns the authenticated user ID and whether it was
// present. Handlers answer 401 when it is missing, which indicates a
// route that was registered outside the protected subtree.
func UserFromContext(ctx context.Context) (int64, bool) {
	userID, ok := ctx.Value(userIDKey).(int64)
	return userID, ok
}

// currentUser fetches the authenticated user ID or writes 401 and
// reports false, which keeps every handler's opening lines identical.
func (s *Server) currentUser(w http.ResponseWriter, r *http.Request) (int64, bool) {
	userID, ok := UserFromContext(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return 0, false
	}
	return userID, true
}

// logRequestError records a failure that happened while handling a
// request, so a 500 in the logs can be traced to its cause.
func logRequestError(r *http.Request, code string, err error) {
	slog.Error("request failed",
		"code", code,
		"method", r.Method,
		"path", r.URL.Path,
		"error", err,
	)
}
