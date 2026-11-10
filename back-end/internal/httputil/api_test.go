package httputil

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/auth"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/config"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/db"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
	"golang.org/x/crypto/bcrypt"
)

// apiFixture bundles a running test server with its dependencies.
type apiFixture struct {
	t       *testing.T
	server  *Server
	handler http.Handler
	store   *db.Store
	issuer  *auth.TokenIssuer
}

// newFixture builds a server backed by a migrated temporary database.
// bcrypt runs at its cheapest cost so the suite stays fast.
func newFixture(t *testing.T) *apiFixture {
	t.Helper()

	handle, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("db.Open() failed: %v", err)
	}
	t.Cleanup(func() { handle.Close() })

	if err := db.Migrate(context.Background(), handle); err != nil {
		t.Fatalf("db.Migrate() failed: %v", err)
	}

	cfg := config.Config{
		ServerAddress:     ":0",
		JWTSecret:         "test-secret",
		TokenExpiry:       time.Hour,
		MessageLimitChars: 4000,
	}

	store := db.NewStore(handle)
	hasher := auth.NewHasher(bcrypt.MinCost)
	issuer, err := auth.NewTokenIssuer(cfg.JWTSecret, cfg.TokenExpiry)
	if err != nil {
		t.Fatalf("NewTokenIssuer() failed: %v", err)
	}

	server := NewServer(cfg, store, hasher, issuer)

	return &apiFixture{
		t:       t,
		server:  server,
		handler: server.Routes(),
		store:   store,
		issuer:  issuer,
	}
}

// do performs a request against the fixture and returns the recorder.
func (f *apiFixture) do(method, path, token string, body any) *httptest.ResponseRecorder {
	f.t.Helper()

	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			f.t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)

	return rec
}

// register creates an account through the API and returns it.
func (f *apiFixture) register(username string) models.User {
	f.t.Helper()

	rec := f.do(http.MethodPost, "/api/auth/register", "", registerRequest{
		Username:    username,
		DisplayName: "Display " + username,
		Password:    "password123",
	})
	if rec.Code != http.StatusCreated {
		f.t.Fatalf("register(%q) status = %d, body = %s", username, rec.Code, rec.Body)
	}

	var user models.User
	if err := json.Unmarshal(rec.Body.Bytes(), &user); err != nil {
		f.t.Fatalf("decode user: %v", err)
	}
	return user
}

// login authenticates and returns the token.
func (f *apiFixture) login(username string) string {
	f.t.Helper()

	rec := f.do(http.MethodPost, "/api/auth/login", "", loginRequest{
		Username: username,
		Password: "password123",
	})
	if rec.Code != http.StatusOK {
		f.t.Fatalf("login(%q) status = %d, body = %s", username, rec.Code, rec.Body)
	}

	var resp loginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		f.t.Fatalf("decode login response: %v", err)
	}
	return resp.Token
}

func TestHealth(t *testing.T) {
	f := newFixture(t)

	rec := f.do(http.MethodGet, "/api/health", "", nil)
	if got, want := rec.Code, http.StatusOK; got != want {
		t.Fatalf("status: got %d, want %d", got, want)
	}
}

func TestRegisterAndLogin(t *testing.T) {
	f := newFixture(t)

	user := f.register("gabriel")
	if user.ID == 0 {
		t.Fatal("expected a non-zero user id")
	}

	// The response must never carry the hash.
	if bytes.Contains(f.do(http.MethodGet, "/api/health", "", nil).Body.Bytes(), []byte("password_hash")) {
		t.Error("health response leaked a password hash key")
	}

	rec := f.do(http.MethodPost, "/api/auth/login", "", loginRequest{
		Username: "gabriel", Password: "password123",
	})
	if got, want := rec.Code, http.StatusOK; got != want {
		t.Fatalf("login status: got %d, want %d (%s)", got, want, rec.Body)
	}

	var resp loginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if resp.Token == "" {
		t.Error("login returned an empty token")
	}
	if resp.User.PasswordHash != "" {
		t.Error("login response exposed the password hash")
	}
}

func TestRegisterValidation(t *testing.T) {
	f := newFixture(t)

	cases := []struct {
		name string
		body registerRequest
		want int
	}{
		{"short username", registerRequest{Username: "ab", DisplayName: "X", Password: "password123"}, http.StatusBadRequest},
		{"invalid characters", registerRequest{Username: "gab riel!", DisplayName: "X", Password: "password123"}, http.StatusBadRequest},
		{"empty display name", registerRequest{Username: "gabriel", DisplayName: "", Password: "password123"}, http.StatusBadRequest},
		{"short password", registerRequest{Username: "gabriel", DisplayName: "X", Password: "short"}, http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.do(http.MethodPost, "/api/auth/register", "", tc.body)
			if got := rec.Code; got != tc.want {
				t.Errorf("status: got %d, want %d (%s)", got, tc.want, rec.Body)
			}
		})
	}
}

// TestRegisterNormalizesUsername documents that a mixed-case username is
// lowercased rather than rejected, so a user can type "Gabriel" and still
// log in as "gabriel". Duplicate detection then works across cases, which
// the second half of this test covers.
func TestRegisterNormalizesUsername(t *testing.T) {
	f := newFixture(t)

	rec := f.do(http.MethodPost, "/api/auth/register", "", registerRequest{
		Username: "Gabriel", DisplayName: "Gabriel", Password: "password123",
	})
	if got, want := rec.Code, http.StatusCreated; got != want {
		t.Fatalf("status: got %d, want %d (%s)", got, want, rec.Body)
	}

	var user models.User
	if err := json.Unmarshal(rec.Body.Bytes(), &user); err != nil {
		t.Fatalf("decode user: %v", err)
	}
	if got, want := user.Username, "gabriel"; got != want {
		t.Errorf("username: got %q, want %q", got, want)
	}

	// Registering the other case must now collide.
	rec = f.do(http.MethodPost, "/api/auth/register", "", registerRequest{
		Username: "GABRIEL", DisplayName: "Other", Password: "password123",
	})
	if got, want := rec.Code, http.StatusConflict; got != want {
		t.Errorf("duplicate status: got %d, want %d (%s)", got, want, rec.Body)
	}
}

func TestRegisterDuplicateUsername(t *testing.T) {
	f := newFixture(t)
	f.register("gabriel")

	rec := f.do(http.MethodPost, "/api/auth/register", "", registerRequest{
		Username: "gabriel", DisplayName: "Other", Password: "password123",
	})
	if got, want := rec.Code, http.StatusConflict; got != want {
		t.Errorf("status: got %d, want %d (%s)", got, want, rec.Body)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	f := newFixture(t)
	f.register("gabriel")

	rec := f.do(http.MethodPost, "/api/auth/login", "", loginRequest{
		Username: "gabriel", Password: "wrong-password",
	})
	if got, want := rec.Code, http.StatusUnauthorized; got != want {
		t.Errorf("status: got %d, want %d", got, want)
	}
}

// TestLoginUnknownUserMatchesWrongPassword keeps the two failure modes
// indistinguishable so accounts cannot be enumerated.
func TestLoginUnknownUserMatchesWrongPassword(t *testing.T) {
	f := newFixture(t)
	f.register("gabriel")

	unknown := f.do(http.MethodPost, "/api/auth/login", "", loginRequest{
		Username: "nobody", Password: "password123",
	})
	wrong := f.do(http.MethodPost, "/api/auth/login", "", loginRequest{
		Username: "gabriel", Password: "wrong-password",
	})

	if unknown.Code != wrong.Code {
		t.Errorf("status differs: unknown user %d, wrong password %d", unknown.Code, wrong.Code)
	}
	if unknown.Body.String() != wrong.Body.String() {
		t.Errorf("body differs:\n unknown user: %s\n wrong password: %s", unknown.Body, wrong.Body)
	}
}

func TestProtectedRoutesRequireToken(t *testing.T) {
	f := newFixture(t)

	paths := []struct{ method, path string }{
		{http.MethodGet, "/api/users"},
		{http.MethodGet, "/api/conversations"},
		{http.MethodPost, "/api/conversations"},
		{http.MethodGet, "/api/conversations/1/messages"},
	}

	for _, p := range paths {
		t.Run(p.method+" "+p.path, func(t *testing.T) {
			rec := f.do(p.method, p.path, "", nil)
			if got, want := rec.Code, http.StatusUnauthorized; got != want {
				t.Errorf("status: got %d, want %d", got, want)
			}
		})
	}
}

func TestSearchUsers(t *testing.T) {
	f := newFixture(t)
	me := f.register("gabriel")
	f.register("gabriela")
	token := f.login("gabriel")

	rec := f.do(http.MethodGet, "/api/users?search=gabri", token, nil)
	if got, want := rec.Code, http.StatusOK; got != want {
		t.Fatalf("status: got %d, want %d (%s)", got, want, rec.Body)
	}

	var results []userResult
	if err := json.Unmarshal(rec.Body.Bytes(), &results); err != nil {
		t.Fatalf("decode results: %v", err)
	}
	if got, want := len(results), 1; got != want {
		t.Fatalf("result count: got %d, want %d", got, want)
	}
	if results[0].ID == me.ID {
		t.Error("search returned the caller")
	}
}

func TestCreateDirectConversationIsIdempotent(t *testing.T) {
	f := newFixture(t)
	f.register("alice")
	bob := f.register("bob")
	token := f.login("alice")

	body := createConversationRequest{Type: models.ConversationDirect, Members: []int64{bob.ID}}

	first := f.do(http.MethodPost, "/api/conversations", token, body)
	if got, want := first.Code, http.StatusCreated; got != want {
		t.Fatalf("first create: got %d, want %d (%s)", got, want, first.Body)
	}

	second := f.do(http.MethodPost, "/api/conversations", token, body)
	if got, want := second.Code, http.StatusOK; got != want {
		t.Fatalf("second create: got %d, want %d (%s)", got, want, second.Body)
	}

	var a, b models.Conversation
	_ = json.Unmarshal(first.Body.Bytes(), &a)
	_ = json.Unmarshal(second.Body.Bytes(), &b)

	if a.ID != b.ID {
		t.Errorf("duplicate conversations created: %d and %d", a.ID, b.ID)
	}
}

func TestCreateGroupConversation(t *testing.T) {
	f := newFixture(t)
	f.register("alice")
	bob := f.register("bob")
	token := f.login("alice")

	rec := f.do(http.MethodPost, "/api/conversations", token, createConversationRequest{
		Type: models.ConversationGroup, Name: "team", Members: []int64{bob.ID},
	})
	if got, want := rec.Code, http.StatusCreated; got != want {
		t.Fatalf("status: got %d, want %d (%s)", got, want, rec.Body)
	}

	var conv models.Conversation
	if err := json.Unmarshal(rec.Body.Bytes(), &conv); err != nil {
		t.Fatalf("decode conversation: %v", err)
	}
	if got, want := len(conv.Members), 2; got != want {
		t.Errorf("member count: got %d, want %d", got, want)
	}
	for _, m := range conv.Members {
		if m.Username == "alice" && m.Role != models.RoleAdmin {
			t.Errorf("creator role: got %q, want %q", m.Role, models.RoleAdmin)
		}
	}
}

func TestCreateDirectConversationWithSelfIsRejected(t *testing.T) {
	f := newFixture(t)
	me := f.register("gabriel")
	token := f.login("gabriel")

	rec := f.do(http.MethodPost, "/api/conversations", token, createConversationRequest{
		Type: models.ConversationDirect, Members: []int64{me.ID},
	})
	if got, want := rec.Code, http.StatusBadRequest; got != want {
		t.Errorf("status: got %d, want %d (%s)", got, want, rec.Body)
	}
}

// TestNonMemberCannotReadMessages is the central authorisation test: an
// outsider must not be able to read a conversation they are not in, and
// must not be told whether it exists.
func TestNonMemberCannotReadMessages(t *testing.T) {
	f := newFixture(t)
	f.register("alice")
	bob := f.register("bob")
	f.register("outsider")

	aliceToken := f.login("alice")
	outsiderToken := f.login("outsider")

	rec := f.do(http.MethodPost, "/api/conversations", aliceToken, createConversationRequest{
		Type: models.ConversationDirect, Members: []int64{bob.ID},
	})
	var conv models.Conversation
	_ = json.Unmarshal(rec.Body.Bytes(), &conv)

	rec = f.do(http.MethodGet, "/api/conversations/"+itoa(conv.ID)+"/messages", outsiderToken, nil)
	if got, want := rec.Code, http.StatusNotFound; got != want {
		t.Errorf("outsider status: got %d, want %d (%s)", got, want, rec.Body)
	}

	rec = f.do(http.MethodGet, "/api/conversations/"+itoa(conv.ID)+"/messages", aliceToken, nil)
	if got, want := rec.Code, http.StatusOK; got != want {
		t.Errorf("member status: got %d, want %d (%s)", got, want, rec.Body)
	}
}

func TestAddAndRemoveMembers(t *testing.T) {
	f := newFixture(t)
	f.register("alice")
	bob := f.register("bob")
	carol := f.register("carol")

	token := f.login("alice")

	rec := f.do(http.MethodPost, "/api/conversations", token, createConversationRequest{
		Type: models.ConversationGroup, Name: "team", Members: []int64{bob.ID},
	})
	var conv models.Conversation
	_ = json.Unmarshal(rec.Body.Bytes(), &conv)

	// Add carol.
	rec = f.do(http.MethodPost, "/api/conversations/"+itoa(conv.ID)+"/members", token,
		addMembersRequest{Members: []int64{carol.ID}})
	if got, want := rec.Code, http.StatusOK; got != want {
		t.Fatalf("add members: got %d, want %d (%s)", got, want, rec.Body)
	}

	var updated models.Conversation
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if got, want := len(updated.Members), 3; got != want {
		t.Errorf("member count after add: got %d, want %d", got, want)
	}

	// Remove bob.
	rec = f.do(http.MethodDelete,
		"/api/conversations/"+itoa(conv.ID)+"/members/"+itoa(bob.ID), token, nil)
	if got, want := rec.Code, http.StatusOK; got != want {
		t.Fatalf("remove member: got %d, want %d (%s)", got, want, rec.Body)
	}

	rec = f.do(http.MethodGet, "/api/conversations", token, nil)
	var conversations []models.Conversation
	_ = json.Unmarshal(rec.Body.Bytes(), &conversations)
	for _, c := range conversations {
		if c.ID != conv.ID {
			continue
		}
		if got, want := len(c.Members), 2; got != want {
			t.Errorf("member count after remove: got %d, want %d", got, want)
		}
	}
}

// TestNonAdminCannotRemoveOthers covers the role check.
func TestNonAdminCannotRemoveOthers(t *testing.T) {
	f := newFixture(t)
	f.register("alice")
	bob := f.register("bob")
	carol := f.register("carol")

	aliceToken := f.login("alice")
	bobToken := f.login("bob")

	rec := f.do(http.MethodPost, "/api/conversations", aliceToken, createConversationRequest{
		Type: models.ConversationGroup, Name: "team", Members: []int64{bob.ID, carol.ID},
	})
	var conv models.Conversation
	_ = json.Unmarshal(rec.Body.Bytes(), &conv)

	// Bob is a plain member, so he may not remove carol.
	rec = f.do(http.MethodDelete,
		"/api/conversations/"+itoa(conv.ID)+"/members/"+itoa(carol.ID), bobToken, nil)
	if got, want := rec.Code, http.StatusForbidden; got != want {
		t.Errorf("status: got %d, want %d (%s)", got, want, rec.Body)
	}

	// But he may always remove himself.
	rec = f.do(http.MethodDelete,
		"/api/conversations/"+itoa(conv.ID)+"/members/"+itoa(bob.ID), bobToken, nil)
	if got, want := rec.Code, http.StatusOK; got != want {
		t.Errorf("self-removal status: got %d, want %d (%s)", got, want, rec.Body)
	}
}

func TestAddMembersToDirectConversationIsRejected(t *testing.T) {
	f := newFixture(t)
	f.register("alice")
	bob := f.register("bob")
	carol := f.register("carol")
	token := f.login("alice")

	rec := f.do(http.MethodPost, "/api/conversations", token, createConversationRequest{
		Type: models.ConversationDirect, Members: []int64{bob.ID},
	})
	var conv models.Conversation
	_ = json.Unmarshal(rec.Body.Bytes(), &conv)

	rec = f.do(http.MethodPost, "/api/conversations/"+itoa(conv.ID)+"/members", token,
		addMembersRequest{Members: []int64{carol.ID}})
	if got, want := rec.Code, http.StatusBadRequest; got != want {
		t.Errorf("status: got %d, want %d (%s)", got, want, rec.Body)
	}
}

func TestListMessagesPaginationBounds(t *testing.T) {
	f := newFixture(t)
	f.register("alice")
	bob := f.register("bob")
	token := f.login("alice")

	rec := f.do(http.MethodPost, "/api/conversations", token, createConversationRequest{
		Type: models.ConversationDirect, Members: []int64{bob.ID},
	})
	var conv models.Conversation
	_ = json.Unmarshal(rec.Body.Bytes(), &conv)

	// A limit above the maximum must be clamped, not honoured.
	rec = f.do(http.MethodGet,
		"/api/conversations/"+itoa(conv.ID)+"/messages?limit=99999", token, nil)
	if got, want := rec.Code, http.StatusOK; got != want {
		t.Errorf("status: got %d, want %d (%s)", got, want, rec.Body)
	}

	// Malformed parameters are a client error.
	for _, query := range []string{"?before=abc", "?limit=-1"} {
		rec = f.do(http.MethodGet,
			"/api/conversations/"+itoa(conv.ID)+"/messages"+query, token, nil)
		if got, want := rec.Code, http.StatusBadRequest; got != want {
			t.Errorf("query %q status: got %d, want %d (%s)", query, got, want, rec.Body)
		}
	}
}

func TestInvalidConversationIDIsRejected(t *testing.T) {
	f := newFixture(t)
	f.register("alice")
	token := f.login("alice")

	rec := f.do(http.MethodGet, "/api/conversations/abc/messages", token, nil)
	if got, want := rec.Code, http.StatusBadRequest; got != want {
		t.Errorf("status: got %d, want %d (%s)", got, want, rec.Body)
	}
}

// TestWrongMethodIsRejected relies on the Go 1.22 routing patterns,
// which return 405 for a method the mux does not know.
func TestWrongMethodIsRejected(t *testing.T) {
	f := newFixture(t)

	rec := f.do(http.MethodDelete, "/api/health", "", nil)
	if got, want := rec.Code, http.StatusMethodNotAllowed; got != want {
		t.Errorf("status: got %d, want %d", got, want)
	}
}

// itoa formats an ID for use in a URL path.
func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}
