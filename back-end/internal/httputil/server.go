package httputil

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/coder/websocket"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/auth"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/config"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/db"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/hub"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/ws"
)

// maxSearchResults limita quantos usuários uma busca devolve. Sem limite,
// um termo amplo como "a" serializaria a tabela de usuários inteira.
const maxSearchResults = 20

// defaultMessagePage e maxMessagePage limitam a paginação do histórico de
// mensagens: o padrão mantém pequena a primeira carga, o máximo impede um
// cliente de pedir a conversa inteira de uma vez.
const (
	defaultMessagePage = 50
	maxMessagePage     = 100
)

// Presence informa quais usuários têm uma conexão aberta agora. O hub a
// implementa; mantê-la como interface aqui faz a camada HTTP não depender
// do pacote hub.
type Presence interface {
	IsOnline(userID int64) bool
}

// TokenValidator valida um bearer token bruto e devolve o ID do usuário.
// Espelha middleware.TokenValidator; a camada HTTP declara a própria cópia
// para que httputil e middleware não importem um ao outro.
type TokenValidator interface {
	ValidateToken(raw string) (int64, error)
}

// Server guarda as dependências comuns a todos os handlers HTTP.
//
// Os handlers são métodos deste tipo, e não funções soltas, para que as
// dependências viajem com eles. Não há estado global, o que torna cada
// handler testável isoladamente.
type Server struct {
	cfg       config.Config
	store     *db.Store
	hasher    *auth.Hasher
	issuer    *auth.TokenIssuer
	validator TokenValidator
	presence  Presence
	hub       RealtimeHub
}

// RealtimeHub é a parte do hub de que a camada HTTP precisa: abrir
// conexões, registrá-las e enviar notificações originadas no REST.
type RealtimeHub interface {
	NewClient(userID int64, conn *websocket.Conn) *hub.Client
	Register(client *hub.Client)
	Unregister(client *hub.Client)
	Submit(ctx context.Context, client *hub.Client, env ws.Envelope, rawSize int) error
	IsOnline(userID int64) bool
	Notify(ctx context.Context, n hub.Notification)
}

// SetHub conecta o hub de tempo real. É definido depois da construção
// porque o próprio hub precisa do store, e porque os testes que não
// exercitam o caminho WebSocket podem deixá-lo nil.
func (s *Server) SetHub(h RealtimeHub) {
	s.hub = h
	s.presence = h
}

// NewServer monta a camada HTTP.
func NewServer(cfg config.Config, store *db.Store, hasher *auth.Hasher, issuer *auth.TokenIssuer) *Server {
	return &Server{
		cfg:       cfg,
		store:     store,
		hasher:    hasher,
		issuer:    issuer,
		validator: issuer,
	}
}

// SetPresence conecta a fonte de presença usada para marcar usuários e
// membros com seu estado online.
func (s *Server) SetPresence(p Presence) {
	s.presence = p
}

// isOnline informa o estado online de um usuário, com false como padrão
// quando nenhuma fonte de presença está conectada.
func (s *Server) isOnline(userID int64) bool {
	if s.presence == nil {
		return false
	}
	return s.presence.IsOnline(userID)
}

// Routes monta o roteador com todos os endpoints registrados.
//
// Os padrões do Go 1.22+ trazem o método e os curingas, então o próprio
// mux rejeita um método errado com 405 e o handler lê o curinga com
// r.PathValue. Nenhum roteador de terceiros é necessário.
//
// A autenticação é aplicada aqui como um invólucro da subárvore protegida,
// e não dentro de cada handler, então deixar uma rota nova desprotegida
// é um ato deliberado, e não um descuido.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// Públicas.
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /ws", s.handleWS)
	mux.HandleFunc("POST /api/auth/register", s.handleRegister)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)

	// Protegidas: tudo abaixo exige um bearer token válido.
	protected := http.NewServeMux()
	protected.HandleFunc("GET /api/users", s.handleSearchUsers)
	protected.HandleFunc("GET /api/conversations", s.handleListConversations)
	protected.HandleFunc("POST /api/conversations", s.handleCreateConversation)
	protected.HandleFunc("POST /api/conversations/{id}/members", s.handleAddMembers)
	protected.HandleFunc("DELETE /api/conversations/{id}/members/{userId}", s.handleRemoveMember)
	protected.HandleFunc("GET /api/conversations/{id}/messages", s.handleListMessages)
	protected.HandleFunc("GET /api/conversations/{id}/messages/search", s.handleSearchMessages)

	mux.Handle("/api/users", s.authenticate(protected))
	mux.Handle("/api/users/", s.authenticate(protected))
	mux.Handle("/api/conversations", s.authenticate(protected))
	mux.Handle("/api/conversations/", s.authenticate(protected))

	return mux
}

// contextKey é um tipo não exportado para a chave de contexto do ID do
// usuário, para que não colida com uma chave definida por outro pacote.
type contextKey struct{}

// userIDKey é onde o ID do usuário autenticado fica guardado.
var userIDKey = contextKey{}

// authenticate rejeita requisições sem um bearer token válido e guarda o
// ID do usuário no contexto da requisição.
//
// Isto duplica middleware.Authenticate por necessidade: middleware importa
// httputil para as respostas de erro, então httputil não pode importar
// middleware de volta sem um ciclo. A camada HTTP tem a própria cópia e
// UserFromContext a lê.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.validator == nil {
			WriteError(w, http.StatusServiceUnavailable,
				"not_configured", "authentication is not configured")
			return
		}

		raw, err := auth.BearerToken(r.Header.Get("Authorization"))
		if err != nil {
			WriteError(w, http.StatusUnauthorized,
				"unauthorized", "missing or malformed authorization header")
			return
		}

		userID, err := s.validator.ValidateToken(raw)
		if err != nil {
			WriteError(w, http.StatusUnauthorized,
				"unauthorized", "invalid or expired token")
			return
		}

		ctx := context.WithValue(r.Context(), userIDKey, userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UserFromContext devolve o ID do usuário autenticado e se ele estava
// presente. Os handlers respondem 401 quando ele falta, o que indica uma
// rota registrada fora da subárvore protegida.
func UserFromContext(ctx context.Context) (int64, bool) {
	userID, ok := ctx.Value(userIDKey).(int64)
	return userID, ok
}

// currentUser obtém o ID do usuário autenticado ou escreve 401 e devolve
// false, o que mantém idênticas as primeiras linhas de todo handler.
func (s *Server) currentUser(w http.ResponseWriter, r *http.Request) (int64, bool) {
	userID, ok := UserFromContext(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return 0, false
	}
	return userID, true
}

// logRequestError registra uma falha ocorrida ao tratar uma requisição,
// para que um 500 nos logs possa ser rastreado até a causa.
func logRequestError(r *http.Request, code string, err error) {
	slog.Error("request failed",
		"code", code,
		"method", r.Method,
		"path", r.URL.Path,
		"error", err,
	)
}
