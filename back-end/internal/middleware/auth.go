package middleware

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/auth"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/httputil"
)

// contextKey é um tipo não exportado para as chaves de contexto definidas
// por este pacote.
//
// Uma chave string correria o risco de colidir com uma chave definida por
// outro pacote ou por uma dependência, sobrescrevendo em silêncio o ID do
// usuário. Um tipo não exportado não pode ser construído fora deste
// pacote, então a colisão é impossível por construção.
type contextKey struct{}

// userIDKey é a chave de contexto sob a qual o ID do usuário autenticado
// fica guardado.
var userIDKey = contextKey{}

// TokenValidator é a parte de auth.TokenIssuer de que este middleware
// precisa. Depender de uma interface estreita em vez do tipo concreto
// mantém o middleware testável sem gerar tokens reais.
type TokenValidator interface {
	ValidateToken(raw string) (int64, error)
}

// Authenticate rejeita requisições sem um bearer token válido e guarda o
// ID do usuário autenticado no contexto da requisição.
//
// A resposta 401 é vaga de propósito: distinguir "sem token" de "token
// expirado" de "usuário desconhecido" diria a um atacante qual parte do
// palpite chegou perto.
func Authenticate(validator TokenValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")

			raw, err := auth.BearerToken(header)
			if err != nil {
				httputil.WriteError(w, http.StatusUnauthorized,
					"unauthorized", "missing or malformed authorization header")
				return
			}

			userID, err := validator.ValidateToken(raw)
			if err != nil {
				slog.Debug("token rejected", "error", err)
				httputil.WriteError(w, http.StatusUnauthorized,
					"unauthorized", "invalid or expired token")
				return
			}

			ctx := context.WithValue(r.Context(), userIDKey, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserFromContext devolve o ID do usuário autenticado.
//
// O segundo valor é false quando o handler foi alcançado sem passar por
// Authenticate, o que é um bug de roteamento e não um erro do cliente; os
// handlers respondem 401 se o virem.
func UserFromContext(ctx context.Context) (int64, bool) {
	userID, ok := ctx.Value(userIDKey).(int64)
	return userID, ok
}
