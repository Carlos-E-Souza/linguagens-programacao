package wsclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fastOptions deixa os tempos curtos para os testes.
var fastOptions = Options{
	BackoffBase:   10 * time.Millisecond,
	BackoffMax:    50 * time.Millisecond,
	ShutdownDelay: 10 * time.Millisecond,
	PingInterval:  time.Hour,
}

// fakeServer é um servidor WebSocket de teste. handle recebe cada conexão aceita e o número dela (1, 2, ...).
func fakeServer(t *testing.T, handle func(n int64, conn *websocket.Conn, r *http.Request)) *httptest.Server {
	t.Helper()
	var count atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer valido" {
			http.Error(w, "token inválido", http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		handle(count.Add(1), conn, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func wsURL(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	u, err := URLFromServer(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// waitState espera o estado chegar pelo canal de estados.
func waitState(t *testing.T, states <-chan string, want string) {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case s := <-states:
			if s == want {
				return
			}
		case <-timeout:
			t.Fatalf("estado %q não chegou", want)
		}
	}
}

func newTestClient(srvURL, token string) (*Client, chan string) {
	states := make(chan string, 32)
	c := New(srvURL, func() string { return token }, fastOptions)
	c.OnState = func(s string) { states <- s }
	return c, states
}

func TestURLFromServer(t *testing.T) {
	cases := map[string]string{
		"http://localhost:8080":     "ws://localhost:8080/ws",
		"https://chat.exemplo.com/": "wss://chat.exemplo.com/ws",
		"https://exemplo.com/base":  "wss://exemplo.com/base/ws",
	}
	for in, want := range cases {
		if got, err := URLFromServer(in); err != nil || got != want {
			t.Errorf("URLFromServer(%q) = %q, %v; esperava %q", in, got, err, want)
		}
	}
	if _, err := URLFromServer("ftp://x"); err == nil {
		t.Error("esquema ftp deveria falhar")
	}
}

func TestSendAndReceive(t *testing.T) {
	srv := fakeServer(t, func(_ int64, conn *websocket.Conn, r *http.Request) {
		// Eco: devolve cada frame recebido.
		for {
			typ, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			conn.Write(r.Context(), typ, data)
		}
	})
	c, states := newTestClient(wsURL(t, srv), "valido")
	if err := c.Send(Envelope{Type: "typing"}); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("Send sem conexão = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	waitState(t, states, StateConnected)

	env, _ := NewEnvelope("typing", map[string]any{"conversation_id": 1, "active": true})
	if err := c.Send(env); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-c.Received():
		if got.Type != "typing" || string(got.Data) != `{"active":true,"conversation_id":1}` {
			t.Errorf("recebido = %s %s", got.Type, got.Data)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("eco não chegou")
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v", err)
	}
	if c.State() != StateDisconnected {
		t.Errorf("estado final = %s", c.State())
	}
}

func TestReconnectsAfterDrop(t *testing.T) {
	srv := fakeServer(t, func(n int64, conn *websocket.Conn, r *http.Request) {
		if n == 1 {
			conn.CloseNow() // derruba a primeira conexão
			return
		}
		conn.Read(r.Context()) // mantém as seguintes abertas
	})
	c, states := newTestClient(wsURL(t, srv), "valido")
	var connects atomic.Int64
	c.OnConnected = func() { connects.Add(1) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	waitState(t, states, StateConnected)
	waitState(t, states, StateConnecting)
	waitState(t, states, StateConnected)
	if got := connects.Load(); got != 2 {
		t.Errorf("OnConnected chamado %d vezes, esperava 2", got)
	}
}

func TestRetriesWhileServerIsDown(t *testing.T) {
	var up atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			http.Error(w, "fora do ar", http.StatusServiceUnavailable)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err == nil {
			conn.Read(r.Context())
		}
	}))
	defer srv.Close()
	c, states := newTestClient(wsURL(t, srv), "valido")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	waitState(t, states, StateDisconnected)
	up.Store(true)
	waitState(t, states, StateConnected)
}

func TestStopsOnUnauthorized(t *testing.T) {
	srv := fakeServer(t, func(int64, *websocket.Conn, *http.Request) {})
	c, _ := newTestClient(wsURL(t, srv), "expirado")

	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background()) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("Run = %v, esperava ErrUnauthorized", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run não parou após 401")
	}
}

func TestServerShutdownTriggersReconnect(t *testing.T) {
	srv := fakeServer(t, func(n int64, conn *websocket.Conn, r *http.Request) {
		if n == 1 {
			conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"server.shutdown"}`))
			conn.Close(websocket.StatusGoingAway, "")
			return
		}
		conn.Read(r.Context())
	})
	c, states := newTestClient(wsURL(t, srv), "valido")
	var connects atomic.Int64
	c.OnConnected = func() { connects.Add(1) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	waitState(t, states, StateConnected)
	waitState(t, states, StateConnecting)
	waitState(t, states, StateConnected)
	if connects.Load() != 2 {
		t.Errorf("conexões = %d", connects.Load())
	}
	select {
	case env := <-c.Received():
		t.Errorf("server.shutdown não deveria ser repassado: %+v", env)
	default:
	}
}

func TestHeartbeatDetectsDeadConnection(t *testing.T) {
	// O servidor aceita mas nunca lê: o pong não volta e o ping expira.
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	srv := fakeServer(t, func(n int64, conn *websocket.Conn, r *http.Request) {
		if n == 1 {
			<-block
			return
		}
		conn.Read(r.Context())
	})
	opts := fastOptions
	opts.PingInterval = 20 * time.Millisecond
	opts.PingTimeout = 50 * time.Millisecond
	states := make(chan string, 32)
	c := New(wsURL(t, srv), func() string { return "valido" }, opts)
	c.OnState = func(s string) { states <- s }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	waitState(t, states, StateConnected)
	waitState(t, states, StateConnecting)
	waitState(t, states, StateConnected)
}

func TestBackoffGrowsWithJitterAndCap(t *testing.T) {
	base, max := time.Second, 30*time.Second
	low := func() float64 { return 0 }
	high := func() float64 { return 1 }
	cases := []struct {
		attempt   int
		min, maxD time.Duration
	}{
		{0, 500 * time.Millisecond, time.Second},
		{1, time.Second, 2 * time.Second},
		{2, 2 * time.Second, 4 * time.Second},
		{5, 15 * time.Second, 30 * time.Second},
		{60, 15 * time.Second, 30 * time.Second},
	}
	for _, tc := range cases {
		if got := backoff(tc.attempt, base, max, low); got != tc.min {
			t.Errorf("backoff(%d) mínimo = %v, esperava %v", tc.attempt, got, tc.min)
		}
		if got := backoff(tc.attempt, base, max, high); got != tc.maxD {
			t.Errorf("backoff(%d) máximo = %v, esperava %v", tc.attempt, got, tc.maxD)
		}
	}
}
