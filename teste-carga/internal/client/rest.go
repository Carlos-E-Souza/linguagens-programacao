// Pacote client fala com o servidor de chat: chamadas REST de preparação
// (cadastro, login, conversas) e uma conexão WebSocket por usuário simulado.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// ErrGroupMismatch indica um grupo com o nome planejado mas outros membros,
// sobra de uma execução com semente ou tamanhos diferentes.
var ErrGroupMismatch = errors.New("grupo existente com membros diferentes do plano; rode `make load-reset` para recomeçar com banco limpo")

// REST é um cliente mínimo da API HTTP do servidor.
type REST struct {
	BaseURL string
	HTTP    *http.Client
}

// RemoteMember é um membro como devolvido por GET /api/conversations.
type RemoteMember struct {
	UserID int64 `json:"user_id"`
}

// RemoteConversation é uma conversa como devolvida por GET /api/conversations.
type RemoteConversation struct {
	ID      int64          `json:"id"`
	Type    string         `json:"type"`
	Name    string         `json:"name"`
	Members []RemoteMember `json:"members"`
}

// apiError é o corpo de erro padrão do servidor.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// statusError é devolvido quando o servidor responde fora da faixa 2xx.
type statusError struct {
	status int
	body   apiError
	raw    string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("servidor respondeu %d: %s", e.status, e.raw)
}

// do envia uma requisição JSON e decodifica a resposta em out (se não nil).
func (c *REST) do(ctx context.Context, method, path, token string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		se := &statusError{status: resp.StatusCode, raw: strings.TrimSpace(string(data))}
		_ = json.Unmarshal(data, &se.body)
		return se
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decodificar resposta de %s %s: %w", method, path, err)
	}
	return nil
}

// EnsureUser cadastra o usuário; se ele já existe, devolve created=false sem erro.
func (c *REST) EnsureUser(ctx context.Context, username, password string) (bool, error) {
	err := c.do(ctx, http.MethodPost, "/api/auth/register", "", map[string]string{
		"username": username, "display_name": username, "password": password,
	}, nil)
	var se *statusError
	if errors.As(err, &se) && se.status == http.StatusConflict && se.body.Code == "username_taken" {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cadastrar %s: %w", username, err)
	}
	return true, nil
}

// Login devolve o token JWT e o id do usuário.
func (c *REST) Login(ctx context.Context, username, password string) (string, int64, error) {
	var out struct {
		Token string `json:"token"`
		User  struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/auth/login", "", map[string]string{
		"username": username, "password": password,
	}, &out); err != nil {
		return "", 0, fmt.Errorf("login de %s: %w", username, err)
	}
	return out.Token, out.User.ID, nil
}

// ListConversations lista as conversas do dono do token, com membros.
func (c *REST) ListConversations(ctx context.Context, token string) ([]RemoteConversation, error) {
	var out []RemoteConversation
	if err := c.do(ctx, http.MethodGet, "/api/conversations", token, nil, &out); err != nil {
		return nil, fmt.Errorf("listar conversas: %w", err)
	}
	return out, nil
}

// CreateConversation cria uma conversa; members não inclui o criador. Para
// "direct" o servidor devolve a conversa existente se o par já tiver uma.
func (c *REST) CreateConversation(ctx context.Context, token, kind, name string, members []int64) (int64, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/conversations", token, map[string]any{
		"type": kind, "name": name, "members": members,
	}, &out); err != nil {
		return 0, fmt.Errorf("criar conversa %s %q: %w", kind, name, err)
	}
	return out.ID, nil
}

// EnsureGroup procura em existing o grupo chamado name. Se achar com o mesmo
// conjunto de membros, devolve seu id; com membros diferentes, ErrGroupMismatch;
// se não achar, found=false e o chamador deve criá-lo.
func EnsureGroup(existing []RemoteConversation, name string, members []int64) (int64, bool, error) {
	want := slices.Clone(members)
	slices.Sort(want)
	for _, c := range existing {
		if c.Type != "group" || c.Name != name {
			continue
		}
		got := make([]int64, 0, len(c.Members))
		for _, m := range c.Members {
			got = append(got, m.UserID)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			return 0, false, fmt.Errorf("%s: %w", name, ErrGroupMismatch)
		}
		return c.ID, true, nil
	}
	return 0, false, nil
}

// WSURL converte a URL HTTP do servidor na URL do endpoint /ws.
func WSURL(serverURL string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("esquema %q não suportado (use http ou https)", u.Scheme)
	}
	u.Path = "/ws"
	return u.String(), nil
}
