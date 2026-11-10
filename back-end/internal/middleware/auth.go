package middleware

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/auth"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/httputil"
)

// contextKey is an unexported type for context keys set by this package.
//
// Using a string key would risk colliding with a key set by another
// package or by a dependency, silently overwriting the user ID. An
// unexported type cannot be constructed outside this package, so the
// collision is impossible by construction.
type contextKey struct{}

// userIDKey is the context key under which the authenticated user ID is
// stored.
var userIDKey = contextKey{}

// TokenValidator is the part of auth.TokenIssuer this middleware needs.
// Depending on a narrow interface rather than the concrete type keeps the
// middleware testable without generating real tokens.
type TokenValidator interface {
	ValidateToken(raw string) (int64, error)
}

// Authenticate rejects requests without a valid bearer token and stores
// the authenticated user ID in the request context.
//
// The 401 response is deliberately vague: distinguishing "no token" from
// "expired token" from "unknown user" would tell an attacker which part
// of their guess was close.
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

// UserFromContext returns the authenticated user ID.
//
// The second return value is false when the handler was reached without
// passing through Authenticate, which is a routing bug rather than a
// client error; handlers answer 401 if they see it.
func UserFromContext(ctx context.Context) (int64, bool) {
	userID, ok := ctx.Value(userIDKey).(int64)
	return userID, ok
}
