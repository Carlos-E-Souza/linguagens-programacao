package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestServer cria um servidor falso e um cliente apontando para ele.
func newTestServer(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient(srv.URL)
}

func TestLoginSendsCredentialsAndDecodesResponse(t *testing.T) {
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/auth/login" {
			t.Errorf("requisição inesperada: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("login não deve enviar Authorization, recebeu %q", got)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["username"] != "ana" || body["password"] != "segredo123" {
			t.Errorf("corpo inesperado: %v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"token":"abc","user":{"id":7,"username":"ana","display_name":"Ana"}}`))
	})

	resp, err := client.Login(context.Background(), "ana", "segredo123")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Token != "abc" || resp.User.ID != 7 || resp.User.DisplayName != "Ana" {
		t.Errorf("resposta inesperada: %+v", resp)
	}
}

func TestRequestSendsBearerToken(t *testing.T) {
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		if r.URL.Query().Get("before") != "40" || r.URL.Query().Get("limit") != "20" {
			t.Errorf("query inesperada: %s", r.URL.RawQuery)
		}
		w.Write([]byte(`[{"id":1,"conversation_id":3,"sequence":39,"body":"oi"}]`))
	})
	client.SetToken("tok")

	messages, err := client.ListMessages(context.Background(), 3, 40, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Sequence != 39 {
		t.Errorf("mensagens inesperadas: %+v", messages)
	}
}

func TestErrorResponseBecomesAPIError(t *testing.T) {
	cases := map[string]string{
		"plano":    `{"code":"username_taken","message":"Nome de usuário já existe"}`,
		"aninhado": `{"error":{"code":"username_taken","message":"Nome de usuário já existe"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusConflict)
				w.Write([]byte(body))
			})
			_, err := client.Register(context.Background(), "ana", "Ana", "segredo123")
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("esperava APIError, veio %v", err)
			}
			if apiErr.Status != 409 || apiErr.Code != "username_taken" || apiErr.Message == "" {
				t.Errorf("erro inesperado: %+v", apiErr)
			}
		})
	}
}

func TestUnauthorizedCallsHookOnlyWhenAuthenticated(t *testing.T) {
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	calls := 0
	client.OnUnauthorized = func() { calls++ }

	// Login sem token: 401 significa credenciais inválidas, não sessão expirada.
	if _, err := client.Login(context.Background(), "ana", "errada"); !IsStatus(err, 401) {
		t.Fatalf("esperava 401, veio %v", err)
	}
	if calls != 0 {
		t.Fatalf("hook chamado no login: %d", calls)
	}

	client.SetToken("expirado")
	if _, err := client.ListConversations(context.Background()); !IsStatus(err, 401) {
		t.Fatalf("esperava 401, veio %v", err)
	}
	if calls != 1 {
		t.Fatalf("hook chamado %d vezes, esperava 1", calls)
	}
}

func TestNetworkFailureBecomesErrNetwork(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	_, err := NewClient(url).ListConversations(context.Background())
	if !errors.Is(err, ErrNetwork) {
		t.Fatalf("esperava ErrNetwork, veio %v", err)
	}
}

func TestEndpointsUseExpectedRoutes(t *testing.T) {
	var got []string
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.RequestURI())
		switch r.Method {
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case http.MethodPost:
			w.Write([]byte(`{"id":5,"type":"group","name":"Equipe"}`))
		default:
			w.Write([]byte(`[]`))
		}
	})
	ctx := context.Background()
	client.SearchUsers(ctx, "a b")
	client.CreateConversation(ctx, ConversationGroup, "Equipe", []int64{2, 3})
	client.AddMembers(ctx, 5, []int64{4})
	client.RemoveMember(ctx, 5, 4)
	client.ListMessages(ctx, 5, 0, 0)

	want := []string{
		"GET /api/users?search=a+b",
		"POST /api/conversations",
		"POST /api/conversations/5/members",
		"DELETE /api/conversations/5/members/4",
		"GET /api/conversations/5/messages",
	}
	if len(got) != len(want) {
		t.Fatalf("rotas = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rota %d = %q, esperava %q", i, got[i], want[i])
		}
	}
}
