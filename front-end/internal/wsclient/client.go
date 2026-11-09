package wsclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Tipos de envelope tratados pelo próprio cliente; os demais são repassados em Received.
const typeServerShutdown = "server.shutdown"

// Estados da conexão, iguais aos valores do evento connection:state.
const (
	StateConnected    = "connected"
	StateConnecting   = "connecting"
	StateDisconnected = "disconnected"
)

var (
	// ErrUnauthorized indica que o servidor recusou o token no handshake; o cliente para de tentar.
	ErrUnauthorized = errors.New("wsclient: token recusado pelo servidor")
	// ErrNotConnected indica que não há conexão aberta para enviar o envelope.
	ErrNotConnected = errors.New("wsclient: sem conexão")
	// ErrSendBufferFull indica que o buffer de saída está cheio.
	ErrSendBufferFull = errors.New("wsclient: buffer de envio cheio")

	errServerShutdown = errors.New("wsclient: servidor desligando")
)

// Envelope é o frame do protocolo: {"type": "...", "data": {...}}.
type Envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// NewEnvelope monta um envelope serializando data.
func NewEnvelope(kind string, data any) (Envelope, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Envelope{}, fmt.Errorf("wsclient: serializar %s: %w", kind, err)
	}
	return Envelope{Type: kind, Data: raw}, nil
}

// Options ajusta os tempos do cliente. Valores zero usam os padrões.
type Options struct {
	PingInterval     time.Duration // intervalo do heartbeat (padrão 25s)
	PingTimeout      time.Duration // prazo para o pong (padrão 10s)
	WriteTimeout     time.Duration // prazo de cada escrita (padrão 10s)
	DialTimeout      time.Duration // prazo do handshake (padrão 10s)
	BackoffBase      time.Duration // primeira espera da reconexão (padrão 1s)
	BackoffMax       time.Duration // espera máxima da reconexão (padrão 30s)
	ShutdownDelay    time.Duration // espera após server.shutdown (padrão 2s)
	SendBufferSize   int           // tamanho do canal de envio (padrão 64)
	ReceivedBuffSize int           // tamanho do canal Received (padrão 64)
}

func (o *Options) withDefaults() {
	set := func(d *time.Duration, v time.Duration) {
		if *d == 0 {
			*d = v
		}
	}
	set(&o.PingInterval, 25*time.Second)
	set(&o.PingTimeout, 10*time.Second)
	set(&o.WriteTimeout, 10*time.Second)
	set(&o.DialTimeout, 10*time.Second)
	set(&o.BackoffBase, time.Second)
	set(&o.BackoffMax, 30*time.Second)
	set(&o.ShutdownDelay, 2*time.Second)
	if o.SendBufferSize == 0 {
		o.SendBufferSize = 64
	}
	if o.ReceivedBuffSize == 0 {
		o.ReceivedBuffSize = 64
	}
}

// Client mantém uma conexão WebSocket com reconexão automática. Run controla o ciclo de vida; Send e Received
// são a entrada e a saída de envelopes.
type Client struct {
	url   string
	token func() string
	opts  Options

	send     chan []byte
	received chan Envelope

	mu        sync.Mutex
	state     string
	connected bool

	// OnState é chamada a cada mudança de estado (connected, connecting, disconnected).
	OnState func(state string)
	// OnConnected é chamada após cada conexão bem-sucedida (ex.: para sincronizar e reenviar a fila).
	OnConnected func()
}

// New cria um cliente para wsURL. token é consultado a cada tentativa de conexão.
func New(wsURL string, token func() string, opts Options) *Client {
	opts.withDefaults()
	return &Client{
		url:      wsURL,
		token:    token,
		opts:     opts,
		send:     make(chan []byte, opts.SendBufferSize),
		received: make(chan Envelope, opts.ReceivedBuffSize),
		state:    StateDisconnected,
	}
}

// URLFromServer converte a URL HTTP do servidor na URL do WebSocket (http→ws, https→wss, caminho /ws).
func URLFromServer(serverURL string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", fmt.Errorf("wsclient: SERVER_URL inválida: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("wsclient: esquema %q não suportado em SERVER_URL", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/ws"
	return u.String(), nil
}

// Received entrega os envelopes recebidos do servidor (exceto os tratados internamente).
func (c *Client) Received() <-chan Envelope {
	return c.received
}

// State devolve o estado atual da conexão.
func (c *Client) State() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Send coloca um envelope no buffer de saída. Falha sem bloquear se não houver conexão ou se o buffer estiver cheio;
// a garantia de entrega fica com a fila de pendentes, que reenvia o que não recebeu ACK.
func (c *Client) Send(env Envelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("wsclient: serializar envelope: %w", err)
	}
	c.mu.Lock()
	connected := c.connected
	c.mu.Unlock()
	if !connected {
		return ErrNotConnected
	}
	select {
	case c.send <- data:
		return nil
	default:
		return ErrSendBufferFull
	}
}

// Run conecta e mantém a conexão até ctx ser cancelado (devolve ctx.Err()) ou o servidor recusar o token
// (devolve ErrUnauthorized). Entre as tentativas espera com backoff exponencial e jitter.
func (c *Client) Run(ctx context.Context) error {
	defer c.setState(StateDisconnected)
	attempt := 0
	for {
		// Primeira tentativa de um ciclo aparece como "conectando"; depois de uma falha, "sem conexão" até voltar.
		if attempt == 0 {
			c.setState(StateConnecting)
		}
		conn, err := c.dial(ctx)
		if err != nil {
			if errors.Is(err, ErrUnauthorized) {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.setState(StateDisconnected)
			if !sleep(ctx, backoff(attempt, c.opts.BackoffBase, c.opts.BackoffMax, rand.Float64)) {
				return ctx.Err()
			}
			attempt++
			continue
		}

		attempt = 0
		c.setConnected(true)
		if c.OnConnected != nil {
			c.OnConnected()
		}
		err = c.serve(ctx, conn)
		c.setConnected(false)

		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, errServerShutdown) {
			// O servidor avisou que vai desligar: dá um tempo para ele voltar antes de tentar de novo.
			c.setState(StateConnecting)
			if !sleep(ctx, c.opts.ShutdownDelay) {
				return ctx.Err()
			}
		}
	}
}

// dial faz o handshake enviando o token no header Authorization.
func (c *Client) dial(ctx context.Context) (*websocket.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, c.opts.DialTimeout)
	defer cancel()
	header := http.Header{}
	if token := c.token(); token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	conn, resp, err := websocket.Dial(dialCtx, c.url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return nil, ErrUnauthorized
		}
		return nil, err
	}
	return conn, nil
}

// serve roda as goroutines de leitura e de escrita/heartbeat até uma delas falhar ou ctx ser cancelado.
func (c *Client) serve(ctx context.Context, conn *websocket.Conn) error {
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errc := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		errc <- c.readLoop(connCtx, conn)
	}()
	go func() {
		defer wg.Done()
		errc <- c.writeLoop(connCtx, conn)
	}()

	var err error
	select {
	case err = <-errc:
	case <-ctx.Done():
		err = ctx.Err()
	}
	cancel()
	if ctx.Err() != nil {
		conn.Close(websocket.StatusNormalClosure, "cliente encerrando")
	} else {
		conn.CloseNow()
	}
	wg.Wait()
	return err
}

func (c *Client) readLoop(ctx context.Context, conn *websocket.Conn) error {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil || env.Type == "" {
			continue // frame inválido: ignora em vez de derrubar a conexão
		}
		if env.Type == typeServerShutdown {
			return errServerShutdown
		}
		select {
		case c.received <- env:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *Client) writeLoop(ctx context.Context, conn *websocket.Conn) error {
	ticker := time.NewTicker(c.opts.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case data := <-c.send:
			writeCtx, cancel := context.WithTimeout(ctx, c.opts.WriteTimeout)
			err := conn.Write(writeCtx, websocket.MessageText, data)
			cancel()
			if err != nil {
				return err
			}
		case <-ticker.C:
			// O pong é lido pela goroutine de leitura; sem resposta no prazo, a conexão é considerada morta.
			pingCtx, cancel := context.WithTimeout(ctx, c.opts.PingTimeout)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil {
				return fmt.Errorf("wsclient: heartbeat: %w", err)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *Client) setConnected(connected bool) {
	c.mu.Lock()
	c.connected = connected
	c.mu.Unlock()
	if connected {
		c.setState(StateConnected)
		return
	}
	// Descarta o que sobrou no buffer: foi escrito para uma conexão que não existe mais.
	for {
		select {
		case <-c.send:
		default:
			return
		}
	}
}

func (c *Client) setState(state string) {
	c.mu.Lock()
	changed := c.state != state
	c.state = state
	c.mu.Unlock()
	if changed && c.OnState != nil {
		c.OnState(state)
	}
}

// backoff calcula a espera da tentativa n: base·2ⁿ limitado a max, com jitter entre metade e o valor cheio.
func backoff(attempt int, base, max time.Duration, random func() float64) time.Duration {
	d := max
	if attempt < 30 {
		if exp := base << attempt; exp > 0 && exp < max {
			d = exp
		}
	}
	return d/2 + time.Duration(random()*float64(d/2))
}

// sleep espera d ou até ctx ser cancelado; devolve false se foi cancelado.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
