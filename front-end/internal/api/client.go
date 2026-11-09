package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// requestTimeout é o tempo máximo de cada requisição REST.
const requestTimeout = 10 * time.Second

// ErrNetwork indica que o servidor não pôde ser alcançado (sem rede, servidor desligado, timeout).
var ErrNetwork = errors.New("api: servidor indisponível")

// APIError é uma resposta de erro do servidor (status 4xx ou 5xx).
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("api: %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("api: status %d", e.Status)
}

// IsStatus informa se err é um APIError com o status indicado.
func IsStatus(err error, status int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == status
}

// Client é o cliente da API REST do servidor. É seguro para uso concorrente.
type Client struct {
	baseURL string
	http    *http.Client

	mu    sync.RWMutex
	token string

	// OnUnauthorized é chamada quando uma requisição autenticada recebe 401 (token expirado ou inválido).
	OnUnauthorized func()
}

// NewClient cria um cliente para o servidor em baseURL (ex.: http://localhost:8080).
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: requestTimeout},
	}
}

// SetToken define o token enviado no header Authorization. Um token vazio desativa a autenticação.
func (c *Client) SetToken(token string) {
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
}

// Token devolve o token atual.
func (c *Client) Token() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

// Register cadastra um usuário (POST /api/auth/register).
func (c *Client) Register(ctx context.Context, username, displayName, password string) (User, error) {
	body := map[string]string{"username": username, "display_name": displayName, "password": password}
	var user User
	err := c.request(ctx, http.MethodPost, "/api/auth/register", body, &user)
	return user, err
}

// Login autentica o usuário (POST /api/auth/login). Não altera o token do cliente; quem chama decide guardá-lo.
func (c *Client) Login(ctx context.Context, username, password string) (LoginResponse, error) {
	body := map[string]string{"username": username, "password": password}
	var resp LoginResponse
	err := c.request(ctx, http.MethodPost, "/api/auth/login", body, &resp)
	return resp, err
}

// SearchUsers busca usuários por nome (GET /api/users?search=).
func (c *Client) SearchUsers(ctx context.Context, term string) ([]User, error) {
	var users []User
	err := c.request(ctx, http.MethodGet, "/api/users?search="+url.QueryEscape(term), nil, &users)
	return users, err
}

// ListConversations lista as conversas do usuário (GET /api/conversations).
func (c *Client) ListConversations(ctx context.Context) ([]Conversation, error) {
	var conversations []Conversation
	err := c.request(ctx, http.MethodGet, "/api/conversations", nil, &conversations)
	return conversations, err
}

// CreateConversation cria uma conversa (POST /api/conversations). Para "direct", o servidor devolve a existente
// quando já houver uma com o mesmo usuário.
func (c *Client) CreateConversation(ctx context.Context, kind, name string, members []int64) (Conversation, error) {
	body := map[string]any{"type": kind, "members": members}
	if name != "" {
		body["name"] = name
	}
	var conversation Conversation
	err := c.request(ctx, http.MethodPost, "/api/conversations", body, &conversation)
	return conversation, err
}

// AddMembers adiciona membros a um grupo (POST /api/conversations/{id}/members).
func (c *Client) AddMembers(ctx context.Context, conversationID int64, members []int64) error {
	path := fmt.Sprintf("/api/conversations/%d/members", conversationID)
	return c.request(ctx, http.MethodPost, path, map[string]any{"members": members}, nil)
}

// RemoveMember remove um membro de um grupo ou faz o próprio usuário sair
// (DELETE /api/conversations/{id}/members/{userId}).
func (c *Client) RemoveMember(ctx context.Context, conversationID, userID int64) error {
	path := fmt.Sprintf("/api/conversations/%d/members/%d", conversationID, userID)
	return c.request(ctx, http.MethodDelete, path, nil, nil)
}

// ListMessages busca o histórico de uma conversa em ordem crescente de sequência
// (GET /api/conversations/{id}/messages). before = 0 pede as mais recentes.
func (c *Client) ListMessages(ctx context.Context, conversationID, before int64, limit int) ([]Message, error) {
	query := url.Values{}
	if before > 0 {
		query.Set("before", strconv.FormatInt(before, 10))
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	path := fmt.Sprintf("/api/conversations/%d/messages", conversationID)
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	var messages []Message
	err := c.request(ctx, http.MethodGet, path, nil, &messages)
	return messages, err
}

// request serializa body em JSON, envia a requisição com o token e decodifica a resposta em out (se não for nil).
// Respostas de erro viram *APIError; falhas de rede viram ErrNetwork.
func (c *Client) request(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("api: serializar corpo: %w", err)
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("api: criar requisição: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	token := c.Token()
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %v", ErrNetwork, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		apiErr := decodeError(resp)
		// Só uma requisição autenticada indica sessão expirada; o 401 do login é "credenciais inválidas".
		if resp.StatusCode == http.StatusUnauthorized && token != "" && c.OnUnauthorized != nil {
			c.OnUnauthorized()
		}
		return apiErr
	}

	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("api: decodificar resposta de %s %s: %w", method, path, err)
	}
	return nil
}

// decodeError lê o corpo de erro do servidor. Aceita {"code","message"} e {"error":{"code","message"}}.
func decodeError(resp *http.Response) *APIError {
	apiErr := &APIError{Status: resp.StatusCode}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil || len(data) == 0 {
		return apiErr
	}
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Error   *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &body) != nil {
		return apiErr
	}
	apiErr.Code, apiErr.Message = body.Code, body.Message
	if body.Error != nil {
		apiErr.Code, apiErr.Message = body.Error.Code, body.Error.Message
	}
	return apiErr
}
