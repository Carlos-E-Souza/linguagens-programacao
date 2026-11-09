package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/api"
	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/cache"
	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/events"
	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/queue"
	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/wsclient"
)

// defaultServerURL é usado quando SERVER_URL não está definida.
const defaultServerURL = "http://localhost:8080"

// pageSize é o tamanho de cada página do histórico.
const pageSize = 50

// maxMessageChars é o tamanho máximo de uma mensagem, o mesmo limite do servidor (MESSAGE_LIMIT_CHARS).
const maxMessageChars = 4000

// App é a estrutura exposta para a interface pelo Wails. Seus métodos públicos viram funções em
// wailsjs/go/main/App.js; os erros devolvidos chegam à interface como promessas rejeitadas com texto amigável.
type App struct {
	ctx       context.Context
	serverURL string
	wsURL     string
	api       *api.Client
	emit      events.Emitter

	mu      sync.Mutex
	session *session
}

// session agrupa o que existe apenas enquanto há um usuário logado. Goroutines em segundo plano guardam o ponteiro
// da sessão em que nasceram e descartam o resultado se ela já tiver terminado.
type session struct {
	user   api.User
	cache  *cache.Cache
	ws     *wsclient.Client
	queue  *queue.Queue
	ctx    context.Context
	cancel context.CancelFunc
	wsDone chan struct{}
	ended  bool
}

// NewApp cria a aplicação lendo SERVER_URL do ambiente.
func NewApp() *App {
	serverURL := strings.TrimRight(os.Getenv("SERVER_URL"), "/")
	if serverURL == "" {
		serverURL = defaultServerURL
	}
	wsURL, err := wsclient.URLFromServer(serverURL)
	if err != nil {
		log.Printf("SERVER_URL inválida (%v); usando %s", err, defaultServerURL)
		serverURL = defaultServerURL
		wsURL, _ = wsclient.URLFromServer(serverURL)
	}
	a := &App{serverURL: serverURL, wsURL: wsURL, api: api.NewClient(serverURL)}
	a.api.OnUnauthorized = func() { go a.expireSession(nil) }
	a.emit = func(string, ...any) {}
	return a
}

// startup é chamado pelo Wails ao iniciar; guarda o contexto usado pelo runtime.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.emit = func(name string, data ...any) { runtime.EventsEmit(ctx, name, data...) }
}

// shutdown é chamado pelo Wails ao fechar a janela: encerra a conexão e fecha o cache, mantendo a sessão salva.
func (a *App) shutdown(context.Context) {
	a.mu.Lock()
	s := a.session
	a.session = nil
	a.mu.Unlock()
	if s != nil {
		a.endSession(s)
	}
}

// Register cadastra um novo usuário. Não faz login; a interface chama Login em seguida.
func (a *App) Register(username, displayName, password string) (api.User, error) {
	user, err := a.api.Register(a.context(), strings.TrimSpace(username), strings.TrimSpace(displayName), password)
	if err != nil {
		return api.User{}, friendly(err, map[int]string{
			http.StatusConflict:   "Esse nome de usuário já está em uso. Escolha outro.",
			http.StatusBadRequest: "Confira os dados: nome de usuário com 3 a 32 letras minúsculas, números ou _, e senha com pelo menos 8 caracteres.",
		})
	}
	return user, nil
}

// Login autentica, abre o cache local do usuário, salva a sessão e conecta o WebSocket.
func (a *App) Login(username, password string) (api.User, error) {
	resp, err := a.api.Login(a.context(), strings.TrimSpace(username), password)
	if err != nil {
		return api.User{}, friendly(err, map[int]string{
			http.StatusUnauthorized: "Usuário ou senha incorretos.",
		})
	}
	if err := a.startSession(resp.Token, resp.User, true); err != nil {
		log.Printf("iniciar sessão: %v", err)
		return api.User{}, errors.New("Não foi possível abrir os dados locais do aplicativo.")
	}
	return resp.User, nil
}

// RestoreSession usa o token salvo do último usuário. Devolve nil se não houver sessão válida. Funciona sem rede:
// o token só é descartado se estiver vencido ou se o servidor recusá-lo depois.
func (a *App) RestoreSession() (*api.User, error) {
	if s := a.current(); s != nil {
		user := s.user
		return &user, nil
	}
	dir, err := cache.Dir()
	if err != nil {
		return nil, nil
	}
	username, err := cache.LoadLastUser(dir)
	if err != nil || username == "" {
		return nil, nil
	}
	c, err := cache.Open(cache.PathFor(dir, username))
	if err != nil {
		log.Printf("restaurar sessão: %v", err)
		return nil, nil
	}
	token, user, ok, err := c.LoadSession(a.context())
	c.Close()
	if err != nil || !ok {
		return nil, nil
	}
	if tokenExpired(token, time.Now()) {
		a.forgetSession(username)
		return nil, nil
	}
	if err := a.startSession(token, user, false); err != nil {
		log.Printf("restaurar sessão: %v", err)
		return nil, nil
	}
	return &user, nil
}

// Logout desconecta, apaga o token salvo e fecha o cache.
func (a *App) Logout() {
	a.mu.Lock()
	s := a.session
	a.session = nil
	a.mu.Unlock()
	if s == nil {
		return
	}
	a.endSession(s)
	a.forgetSession(s.user.Username)
}

// CurrentUser devolve o usuário logado, ou nil.
func (a *App) CurrentUser() *api.User {
	s := a.current()
	if s == nil {
		return nil
	}
	user := s.user
	return &user
}

// ConnectionState devolve connected, connecting ou disconnected.
func (a *App) ConnectionState() string {
	s := a.current()
	if s == nil {
		return events.StateDisconnected
	}
	return s.ws.State()
}

// ListConversations devolve as conversas do cache na hora e atualiza do servidor em segundo plano, emitindo
// conversation:updated para cada conversa que mudou e conversation:removed para as que sumiram.
func (a *App) ListConversations() ([]api.Conversation, error) {
	s := a.current()
	if s == nil {
		return nil, errNoSession
	}
	conversations, err := s.cache.ListConversations(s.ctx)
	if err != nil {
		log.Printf("listar conversas do cache: %v", err)
		return nil, errors.New("Não foi possível ler as conversas salvas.")
	}
	go a.refreshConversations(s, conversations)
	return nonNil(conversations), nil
}

func (a *App) refreshConversations(s *session, cached []api.Conversation) {
	fresh, err := a.api.ListConversations(s.ctx)
	if err != nil {
		if !errors.Is(err, api.ErrNetwork) && s.ctx.Err() == nil {
			log.Printf("atualizar conversas: %v", err)
		}
		return
	}
	removed, err := s.cache.ReplaceConversations(s.ctx, fresh)
	if err != nil {
		log.Printf("salvar conversas: %v", err)
		return
	}
	saved, err := s.cache.ListConversations(s.ctx)
	if err != nil || !a.isCurrent(s) {
		return
	}
	before := make(map[int64]string, len(cached))
	for _, conv := range cached {
		before[conv.ID] = fingerprint(conv)
	}
	for _, conv := range saved {
		if before[conv.ID] != fingerprint(conv) {
			a.emit(events.ConversationUpdated, conv)
		}
	}
	for _, id := range removed {
		a.emit(events.ConversationRemoved, events.ConversationRemovedPayload{ConversationID: id})
	}
}

// LoadMessages devolve uma página do histórico em ordem crescente, com sequência menor que before (0 = mais
// recentes). Usa o cache quando ele tem a página completa; senão busca no servidor e guarda. Sem rede, devolve o
// que houver no cache.
func (a *App) LoadMessages(conversationID, before int64) ([]api.Message, error) {
	s := a.current()
	if s == nil {
		return nil, errNoSession
	}
	cached, err := s.cache.ListMessages(s.ctx, conversationID, before, pageSize)
	if err != nil {
		log.Printf("ler mensagens do cache: %v", err)
		return nil, errors.New("Não foi possível ler as mensagens salvas.")
	}

	upper := before
	if upper == 0 {
		conv, ok, err := s.cache.GetConversation(s.ctx, conversationID)
		if err == nil && ok {
			upper = conv.LastSequence + 1
		}
	}
	if upper > 0 && pageComplete(cached, upper, pageSize) {
		return nonNil(cached), nil
	}

	fresh, err := a.api.ListMessages(s.ctx, conversationID, before, pageSize)
	if err != nil {
		if errors.Is(err, api.ErrNetwork) {
			return nonNil(cached), nil
		}
		return nil, friendly(err, map[int]string{
			http.StatusForbidden: "Você não participa mais desta conversa.",
			http.StatusNotFound:  "Esta conversa não existe mais.",
		})
	}
	if err := s.cache.SaveMessages(s.ctx, fresh); err != nil {
		log.Printf("salvar mensagens: %v", err)
		return nonNil(fresh), nil
	}
	messages, err := s.cache.ListMessages(s.ctx, conversationID, before, pageSize)
	if err != nil {
		return nonNil(fresh), nil
	}
	return nonNil(messages), nil
}

// pageComplete informa se as mensagens do cache formam uma página sem buracos logo abaixo de upper: sequências
// consecutivas a partir de upper-1, com limit itens ou chegando à primeira mensagem da conversa.
func pageComplete(messages []api.Message, upper int64, limit int) bool {
	expected := upper - 1
	count := 0
	for i := len(messages) - 1; i >= 0; i-- {
		seq := messages[i].Sequence
		if seq == 0 {
			continue // pendente, ainda sem sequência
		}
		if seq != expected {
			return false
		}
		expected--
		count++
	}
	return count >= limit || expected == 0
}

// startSession abre o cache, guarda a sessão (se fresh) e inicia a conexão WebSocket.
func (a *App) startSession(token string, user api.User, fresh bool) error {
	dir, err := cache.Dir()
	if err != nil {
		return err
	}
	c, err := cache.Open(cache.PathFor(dir, user.Username))
	if err != nil {
		return err
	}
	if fresh {
		if err := c.SaveSession(a.context(), token, user); err != nil {
			c.Close()
			return err
		}
	}
	if err := cache.SaveLastUser(dir, user.Username); err != nil {
		log.Printf("salvar último usuário: %v", err)
	}

	a.api.SetToken(token)
	ctx, cancel := context.WithCancel(a.context())
	s := &session{user: user, cache: c, ctx: ctx, cancel: cancel, wsDone: make(chan struct{})}
	s.ws = wsclient.New(a.wsURL, a.api.Token, wsclient.Options{})
	s.ws.OnState = func(state string) {
		if a.isCurrent(s) {
			a.emit(events.ConnectionState, state)
		}
	}
	s.queue = queue.New(c, func(msg api.Message) error { return sendMessage(s.ws, msg) }, queue.Options{})
	// A cada (re)conexão a fila inteira é reenviada; em goroutine para não atrasar o início da leitura.
	s.ws.OnConnected = func() { go s.queue.ResendAll(ctx) }

	a.mu.Lock()
	old := a.session
	a.session = s
	a.mu.Unlock()
	if old != nil {
		a.endSession(old)
	}

	go a.consume(s)
	go s.queue.Run(ctx)
	go func() {
		err := s.ws.Run(ctx)
		close(s.wsDone)
		if errors.Is(err, wsclient.ErrUnauthorized) {
			a.expireSession(s)
		}
	}()
	return nil
}

// SendMessage grava a mensagem como pendente, coloca na fila e envia se houver conexão. Devolve a mensagem na hora
// (status pending) para a interface exibir; o ACK chega depois pelo evento message:updated.
func (a *App) SendMessage(conversationID int64, body string) (api.Message, error) {
	s := a.current()
	if s == nil {
		return api.Message{}, errNoSession
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return api.Message{}, errors.New("Escreva uma mensagem antes de enviar.")
	}
	if n := len([]rune(body)); n > maxMessageChars {
		return api.Message{}, fmt.Errorf("A mensagem tem %d caracteres; o limite é %d.", n, maxMessageChars)
	}
	if _, ok, err := s.cache.GetConversation(s.ctx, conversationID); err != nil || !ok {
		return api.Message{}, errors.New("Esta conversa não está mais disponível.")
	}
	msg := api.Message{
		ConversationID: conversationID,
		SenderID:       s.user.ID,
		ClientID:       uuid.NewString(),
		Body:           body,
		CreatedAt:      time.Now().UTC(),
		Status:         api.StatusPending,
	}
	if err := s.queue.Add(s.ctx, msg); err != nil {
		log.Printf("enfileirar mensagem: %v", err)
		return api.Message{}, errors.New("Não foi possível guardar a mensagem para envio.")
	}
	return msg, nil
}

// RetryMessage coloca de novo na fila uma mensagem que falhou, com o mesmo client_id.
func (a *App) RetryMessage(clientID string) (api.Message, error) {
	s := a.current()
	if s == nil {
		return api.Message{}, errNoSession
	}
	msg, ok, err := s.cache.GetMessage(s.ctx, clientID)
	if err != nil || !ok {
		return api.Message{}, errors.New("Esta mensagem não existe mais.")
	}
	if msg.Status != api.StatusFailed {
		return msg, nil
	}
	if err := s.queue.Add(s.ctx, msg); err != nil {
		log.Printf("reenfileirar mensagem: %v", err)
		return api.Message{}, errors.New("Não foi possível guardar a mensagem para envio.")
	}
	msg.Status = api.StatusPending
	return msg, nil
}

// sendMessage monta o envelope message.send e o entrega ao WebSocket.
func sendMessage(ws *wsclient.Client, msg api.Message) error {
	env, err := wsclient.NewEnvelope("message.send", map[string]any{
		"client_id":       msg.ClientID,
		"conversation_id": msg.ConversationID,
		"body":            msg.Body,
	})
	if err != nil {
		return err
	}
	return ws.Send(env)
}

// consume trata os envelopes recebidos pelo WebSocket. message.new, message.status, presence, typing e sync.end
// entram nas Fases 7 e 8.
func (a *App) consume(s *session) {
	for {
		select {
		case env := <-s.ws.Received():
			switch env.Type {
			case "message.ack":
				a.handleAck(s, env.Data)
			case "error":
				a.handleServerError(s, env.Data)
			}
		case <-s.ctx.Done():
			return
		}
	}
}

// handleAck tira a mensagem da fila, grava id, sequência e horário do servidor e avisa a interface.
func (a *App) handleAck(s *session, data json.RawMessage) {
	var ack struct {
		ClientID       string    `json:"client_id"`
		ID             int64     `json:"id"`
		ConversationID int64     `json:"conversation_id"`
		Sequence       int64     `json:"sequence"`
		CreatedAt      time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(data, &ack); err != nil || ack.ClientID == "" {
		log.Printf("message.ack inválido: %s", data)
		return
	}
	if err := s.queue.Ack(s.ctx, ack.ClientID); err != nil {
		log.Printf("remover da fila: %v", err)
	}
	msg, ok, err := s.cache.AckMessage(s.ctx, ack.ClientID, ack.ID, ack.Sequence, ack.CreatedAt)
	if err != nil || !ok {
		if err != nil {
			log.Printf("gravar ACK: %v", err)
		}
		return
	}
	if a.isCurrent(s) {
		a.emit(events.MessageUpdated, events.MessageUpdatedPayload{
			ConversationID: msg.ConversationID, ClientID: msg.ClientID, ID: msg.ID, Sequence: msg.Sequence,
			Status: msg.Status, CreatedAt: msg.CreatedAt,
		})
	}
}

// handleServerError trata o envelope error. Com client_id, a mensagem falhou de vez: sai da fila e vira failed.
func (a *App) handleServerError(s *session, data json.RawMessage) {
	var serverErr struct {
		Code     string `json:"code"`
		Message  string `json:"message"`
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(data, &serverErr); err != nil {
		log.Printf("error inválido: %s", data)
		return
	}
	log.Printf("erro do servidor: %s %s", serverErr.Code, serverErr.Message)
	if serverErr.ClientID == "" {
		return
	}
	if err := s.queue.Fail(s.ctx, serverErr.ClientID); err != nil {
		log.Printf("remover da fila: %v", err)
	}
	if err := s.cache.UpdateStatus(s.ctx, serverErr.ClientID, api.StatusFailed); err != nil {
		log.Printf("marcar falha: %v", err)
	}
	msg, ok, err := s.cache.GetMessage(s.ctx, serverErr.ClientID)
	if err != nil || !ok || !a.isCurrent(s) {
		return
	}
	reason := serverErr.Message
	if reason == "" {
		reason = "O servidor recusou a mensagem."
	}
	a.emit(events.MessageUpdated, events.MessageUpdatedPayload{
		ConversationID: msg.ConversationID, ClientID: msg.ClientID, Status: api.StatusFailed,
		CreatedAt: msg.CreatedAt, Error: reason,
	})
}

// endSession para a conexão e fecha o cache. Não apaga a sessão salva.
func (a *App) endSession(s *session) {
	s.cancel()
	<-s.wsDone
	s.cache.Close()
}

// forgetSession apaga o token salvo do usuário e o registro de último usuário.
func (a *App) forgetSession(username string) {
	a.api.SetToken("")
	dir, err := cache.Dir()
	if err != nil {
		return
	}
	if c, err := cache.Open(cache.PathFor(dir, username)); err == nil {
		c.ClearSession(context.Background())
		c.Close()
	}
	cache.ClearLastUser(dir)
}

// expireSession é chamada quando o servidor recusa o token: encerra a sessão e avisa a interface. target indica a
// sessão que recebeu a recusa; nil significa a sessão atual. Uma recusa de sessão antiga é ignorada.
func (a *App) expireSession(target *session) {
	a.mu.Lock()
	s := a.session
	if s == nil || s.ended || (target != nil && target != s) {
		a.mu.Unlock()
		return
	}
	s.ended = true
	a.session = nil
	a.mu.Unlock()

	a.endSession(s)
	a.forgetSession(s.user.Username)
	a.emit(events.SessionExpired)
}

func (a *App) current() *session {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.session
}

func (a *App) isCurrent(s *session) bool {
	return a.current() == s
}

func (a *App) context() context.Context {
	if a.ctx == nil {
		return context.Background()
	}
	return a.ctx
}

var errNoSession = errors.New("Sua sessão terminou. Entre novamente.")

// friendly traduz um erro da API para uma mensagem para o usuário. byStatus sobrepõe a mensagem de cada status.
func friendly(err error, byStatus map[int]string) error {
	if errors.Is(err, api.ErrNetwork) {
		return errors.New("Não foi possível falar com o servidor. Verifique sua conexão e tente novamente.")
	}
	var apiErr *api.APIError
	if errors.As(err, &apiErr) {
		if msg, ok := byStatus[apiErr.Status]; ok {
			return errors.New(msg)
		}
		if apiErr.Message != "" {
			return errors.New(apiErr.Message)
		}
		if apiErr.Status >= 500 {
			return errors.New("O servidor encontrou um problema. Tente novamente em instantes.")
		}
	}
	log.Printf("erro inesperado: %v", err)
	return errors.New("Algo deu errado. Tente novamente.")
}

// tokenExpired lê o campo exp do JWT sem validar a assinatura (quem valida é o servidor). Um token ilegível é
// tratado como não expirado: o servidor decide na primeira requisição.
func tokenExpired(token string, now time.Time) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return false
	}
	return now.Unix() >= claims.Exp
}

// fingerprint resume os campos de uma conversa que a interface exibe, para detectar mudanças.
func fingerprint(conv api.Conversation) string {
	data, _ := json.Marshal(conv)
	return fmt.Sprintf("%x", data)
}

// nonNil garante que a interface receba [] em vez de null.
func nonNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}
