package middleware

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubValidator stands in for auth.TokenIssuer so the middleware can be
// tested without minting real JWTs.
type stubValidator struct {
	userID int64
	err    error
}

func (s stubValidator) ValidateToken(raw string) (int64, error) {
	if s.err != nil {
		return 0, s.err
	}
	return s.userID, nil
}

// echoUserID is a protected handler that reports the user ID it was
// given, so the test can prove the value travelled through the context.
func echoUserID(w http.ResponseWriter, r *http.Request) {
	userID, ok := UserFromContext(r.Context())
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]int64{"user_id": userID})
}

func TestAuthenticateAcceptsValidToken(t *testing.T) {
	handler := Authenticate(stubValidator{userID: 7})(http.HandlerFunc(echoUserID))

	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	req.Header.Set("Authorization", "Bearer valid.token.here")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got, want := rec.Code, http.StatusOK; got != want {
		t.Fatalf("status: got %d, want %d", got, want)
	}

	var body map[string]int64
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if got, want := body["user_id"], int64(7); got != want {
		t.Errorf("user id in context: got %d, want %d", got, want)
	}
}

func TestAuthenticateRejectsBadRequests(t *testing.T) {
	cases := []struct {
		name   string
		header string
		stub   stubValidator
	}{
		{"missing header", "", stubValidator{userID: 1}},
		{"wrong scheme", "Basic abc", stubValidator{userID: 1}},
		{"no token after scheme", "Bearer ", stubValidator{userID: 1}},
		{"invalid token", "Bearer bad", stubValidator{err: errors.New("invalid")}},
		{"expired token", "Bearer old", stubValidator{err: errors.New("expired")}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := Authenticate(tc.stub)(http.HandlerFunc(echoUserID))

			req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if got, want := rec.Code, http.StatusUnauthorized; got != want {
				t.Errorf("status: got %d, want %d", got, want)
			}
		})
	}
}

// TestUserFromContextWithoutMiddleware documents the failure mode a
// routing mistake would produce.
func TestUserFromContextWithoutMiddleware(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	if _, ok := UserFromContext(req.Context()); ok {
		t.Error("expected no user id in a bare request context")
	}
}
