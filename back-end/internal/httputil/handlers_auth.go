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

// usernamePattern restringe os usernames a letras minúsculas, dígitos e
// sublinhado. A restrição mantém menções e URLs previsíveis e evita
// surpresas de normalização Unicode num índice UNIQUE.
var usernamePattern = regexp.MustCompile(`^[a-z0-9_]{3,32}$`)

// maxDisplayNameLength limita o nome de exibição para que ele não sirva
// para inundar a tela de um cliente com megabytes de texto.
const maxDisplayNameLength = 64

// registerRequest é o corpo de POST /api/auth/register.
type registerRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
}

// loginRequest é o corpo de POST /api/auth/login.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// loginResponse é o que um login bem-sucedido devolve. É o único lugar
// em que o cliente recebe o token, e o objeto do usuário não traz hash.
type loginResponse struct {
	Token string      `json:"token"`
	User  models.User `json:"user"`
}

// handleHealth responde GET /api/health. Não tem dependências, então não
// pode falhar por um motivo que não seja o processo estar fora do ar.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleRegister cria uma conta.
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

// handleLogin confere as credenciais e devolve um token.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := ReadJSON(w, r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}

	req.Username = strings.ToLower(strings.TrimSpace(req.Username))

	// Os dois modos de falha respondem igual: revelar que um username não
	// existe permitiria a um atacante enumerar contas.
	unauthorized := func() {
		WriteError(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
	}

	user, err := s.store.GetUserByUsername(r.Context(), req.Username)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			// Calcular o hash mesmo assim mantém o tempo de resposta
			// parecido exista ou não a conta, então o tempo também não a
			// revela.
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
