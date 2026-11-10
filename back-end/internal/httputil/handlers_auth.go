package httputil

import (
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/auth"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/db"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
)

// usernamePattern constrains usernames to lowercase letters, digits and
// underscores. Restricting them keeps mentions and URLs predictable and
// avoids Unicode normalisation surprises in a UNIQUE index.
var usernamePattern = regexp.MustCompile(`^[a-z0-9_]{3,32}$`)

// maxDisplayNameLength bounds the display name so it cannot be used to
// flood a client's rendering with megabytes of text.
const maxDisplayNameLength = 64

// registerRequest is the body of POST /api/auth/register.
type registerRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
}

// loginRequest is the body of POST /api/auth/login.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// loginResponse is what a successful login returns. The token is the
// only place the client gets it, and the user object carries no hash.
type loginResponse struct {
	Token string      `json:"token"`
	User  models.User `json:"user"`
}

// handleHealth answers GET /api/health. It has no dependencies so it
// cannot fail for a reason unrelated to the process being up.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleRegister creates an account.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := ReadJSON(w, r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}

	req.Username = strings.ToLower(strings.TrimSpace(req.Username))
	req.DisplayName = strings.TrimSpace(req.DisplayName)

	if !usernamePattern.MatchString(req.Username) {
		WriteError(w, http.StatusBadRequest, "invalid_username",
			"username must be 3-32 characters of lowercase letters, digits or underscore")
		return
	}
	if req.DisplayName == "" || len(req.DisplayName) > maxDisplayNameLength {
		WriteError(w, http.StatusBadRequest, "invalid_display_name",
			"display_name is required and must be at most 64 characters")
		return
	}

	hash, err := s.hasher.HashPassword(req.Password)
	if err != nil {
		if errors.Is(err, auth.ErrWeakPassword) {
			WriteError(w, http.StatusBadRequest, "weak_password", err.Error())
			return
		}
		WriteError(w, http.StatusBadRequest, "invalid_password", err.Error())
		return
	}

	user, err := s.store.CreateUser(r.Context(), req.Username, req.DisplayName, hash)
	if err != nil {
		if errors.Is(err, db.ErrConflict) {
			WriteError(w, http.StatusConflict, "username_taken", "that username is already taken")
			return
		}
		logRequestError(r, "create_user", err)
		WriteError(w, http.StatusInternalServerError, "internal_error", "could not create the account")
		return
	}

	WriteJSON(w, http.StatusCreated, user)
}

// handleLogin verifies credentials and returns a token.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := ReadJSON(w, r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}

	req.Username = strings.ToLower(strings.TrimSpace(req.Username))

	// Both failure modes answer identically: revealing that a username
	// does not exist would let an attacker enumerate accounts.
	unauthorized := func() {
		WriteError(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
	}

	user, err := s.store.GetUserByUsername(r.Context(), req.Username)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			// Hashing anyway keeps the response time similar whether or
			// not the account exists, so timing does not leak it either.
			_ = s.hasher.CheckPassword("$2a$10$invalidinvalidinvalidinvalidinvalidinvalidinvalidinvalidinv", req.Password)
			unauthorized()
			return
		}
		logRequestError(r, "get_user_by_username", err)
		WriteError(w, http.StatusInternalServerError, "internal_error", "could not complete the login")
		return
	}

	if err := s.hasher.CheckPassword(user.PasswordHash, req.Password); err != nil {
		unauthorized()
		return
	}

	if s.issuer == nil {
		WriteError(w, http.StatusServiceUnavailable, "not_configured", "token issuing is not configured")
		return
	}

	token, err := s.issuer.GenerateToken(user.ID)
	if err != nil {
		logRequestError(r, "generate_token", err)
		WriteError(w, http.StatusInternalServerError, "internal_error", "could not issue a token")
		return
	}

	slog.Info("user logged in", "user_id", user.ID, "username", user.Username)

	WriteJSON(w, http.StatusOK, loginResponse{Token: token, User: user})
}
