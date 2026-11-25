package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Carlos-E-Souza/linguagens-programacao/teste-carga/internal/metrics"
	"github.com/Carlos-E-Souza/linguagens-programacao/teste-carga/internal/protocol"
)

func TestWSURL(t *testing.T) {
	cases := map[string]string{
		"http://h:8080":  "ws://h:8080/ws",
		"https://h":      "wss://h/ws",
		"http://h:8080/": "ws://h:8080/ws",
	}
	for in, want := range cases {
		got, err := WSURL(in)
		if err != nil || got != want {
			t.Fatalf("WSURL(%q) = %q, %v; esperado %q", in, got, err, want)
		}
	}
	if _, err := WSURL("ftp://h"); err == nil {
		t.Fatal("esperava erro para ftp://")
	}
}

// request guarda o que o servidor falso recebeu.
type request struct {
	method, path, auth string
	body               map[string]any
}

func fakeREST(t *testing.T, status int, reply string) (*REST, *request) {
	t.Helper()
	got := &request{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path, got.auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return &REST{BaseURL: srv.URL, HTTP: srv.Client()}, got
}

func TestRESTFlow(t *testing.T) {
	ctx := context.Background()

	c, got := fakeREST(t, http.StatusCreated, `{"id":7,"username":"carga_u000"}`)
	created, err := c.EnsureUser(ctx, "carga_u000", "carga-senha-123")
	if err != nil || !created {
		t.Fatalf("EnsureUser = %v, %v", created, err)
	}
	if got.method != "POST" || got.path != "/api/auth/register" || got.body["username"] != "carga_u000" ||
		got.body["display_name"] != "carga_u000" || got.body["password"] != "carga-senha-123" {
		t.Fatalf("register inesperado: %+v", got)
	}

	c, got = fakeREST(t, http.StatusOK, `{"token":"t","user":{"id":7}}`)
	token, id, err := c.Login(ctx, "carga_u000", "carga-senha-123")
	if err != nil || token != "t" || id != 7 {
		t.Fatalf("Login = %q, %d, %v", token, id, err)
	}
	if got.method != "POST" || got.path != "/api/auth/login" || got.body["username"] != "carga_u000" {
		t.Fatalf("login inesperado: %+v", got)
	}

	c, got = fakeREST(t, http.StatusCreated, `{"id":9}`)
	convID, err := c.CreateConversation(ctx, "t", "group", "carga_g00", []int64{2, 3})
	if err != nil || convID != 9 {
		t.Fatalf("CreateConversation = %d, %v", convID, err)
	}
	if got.path != "/api/conversations" || got.auth != "Bearer t" || got.body["type"] != "group" || got.body["name"] != "carga_g00" {
		t.Fatalf("create inesperado: %+v", got)
	}
	if members, _ := got.body["members"].([]any); len(members) != 2 {
		t.Fatalf("members = %v", got.body["members"])
	}

	c, got = fakeREST(t, http.StatusOK, `[{"id":9,"type":"group","name":"carga_g00","members":[{"user_id":1},{"user_id":2}]}]`)
	list, err := c.ListConversations(ctx, "t")
	if err != nil || len(list) != 1 || list[0].Name != "carga_g00" || len(list[0].Members) != 2 || list[0].Members[1].UserID != 2 {
		t.Fatalf("ListConversations = %+v, %v", list, err)
	}
	if got.method != "GET" || got.auth != "Bearer t" {
		t.Fatalf("list inesperado: %+v", got)
	}
}

func TestEnsureUserExisting(t *testing.T) {
	c, _ := fakeREST(t, http.StatusConflict, `{"code":"username_taken","message":"x"}`)
	created, err := c.EnsureUser(context.Background(), "carga_u000", "carga-senha-123")
	if err != nil || created {
		t.Fatalf("EnsureUser com usuário existente = %v, %v", created, err)
	}

	c, _ = fakeREST(t, http.StatusConflict, `{"code":"other","message":"x"}`)
	if _, err := c.EnsureUser(context.Background(), "carga_u000", "carga-senha-123"); err == nil {
		t.Fatal("409 com outro código deveria ser erro")
	}
}

func TestRESTErrorStatus(t *testing.T) {
	c, _ := fakeREST(t, http.StatusInternalServerError, `{"code":"internal_error","message":"x"}`)
	_, _, err := c.Login(context.Background(), "a", "b")
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("erro = %v, esperado contendo 500", err)
	}
}

func remote(id int64, kind, name string, members ...int64) RemoteConversation {
	rc := RemoteConversation{ID: id, Type: kind, Name: name}
	for _, m := range members {
		rc.Members = append(rc.Members, RemoteMember{UserID: m})
	}
	return rc
}

func TestEnsureGroupReuse(t *testing.T) {
	existing := []RemoteConversation{
		remote(4, "direct", "", 1, 2),
		remote(9, "group", "carga_g00", 3, 1, 2),
	}
	id, found, err := EnsureGroup(existing, "carga_g00", []int64{1, 2, 3})
	if err != nil || !found || id != 9 {
		t.Fatalf("EnsureGroup = %d, %v, %v", id, found, err)
	}
	_, found, err = EnsureGroup(existing, "carga_g01", []int64{1, 2, 3})
	if err != nil || found {
		t.Fatalf("grupo inexistente: found=%v err=%v", found, err)
	}
}

func TestEnsureGroupMismatch(t *testing.T) {
	existing := []RemoteConversation{remote(9, "group", "carga_g00", 1, 2, 3)}
	_, _, err := EnsureGroup(existing, "carga_g00", []int64{1, 2, 4})
	if !errors.Is(err, ErrGroupMismatch) {
		t.Fatalf("erro = %v, esperado ErrGroupMismatch", err)
	}
}

// fakeWS responde a cada message.send com ack e message.new (de outro
// remetente) e guarda os recibos recebidos.
type fakeWS struct {
	mu       sync.Mutex
	auth     string
	receipts []protocol.Envelope
	shutdown bool
}

func (f *fakeWS) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auth = r.Header.Get("Authorization")
		shutdown := f.shutdown
		f.mu.Unlock()
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		if shutdown {
			_ = writeEnvelope(ctx, conn, protocol.TypeServerShutdown, nil)
			time.Sleep(50 * time.Millisecond)
			return
		}
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var env protocol.Envelope
			_ = json.Unmarshal(data, &env)
			switch env.Type {
			case protocol.TypeMessageSend:
				var p protocol.SendPayload
				_ = json.Unmarshal(env.Data, &p)
				_ = writeEnvelope(ctx, conn, protocol.TypeMessageAck, protocol.AckPayload{ClientID: p.ClientID, ID: 1, ConversationID: p.ConversationID, Sequence: 3})
				_ = writeEnvelope(ctx, conn, protocol.TypeMessageNew, protocol.Message{ID: 2, ConversationID: p.ConversationID, Sequence: 4, SenderID: 99, ClientID: "outro"})
			case protocol.TypeMessageDelivered, protocol.TypeMessageRead:
				f.mu.Lock()
				f.receipts = append(f.receipts, env)
				f.mu.Unlock()
			}
		}
	}
}

func writeEnvelope(ctx context.Context, conn *websocket.Conn, kind string, payload any) error {
	env, err := protocol.NewEnvelope(kind, payload)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(env)
	return conn.Write(ctx, websocket.MessageText, data)
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condição não atingida em 2s")
}

func TestUserSendAndReceive(t *testing.T) {
	f := &fakeWS{}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	wsURL, _ := WSURL(srv.URL)

	rec := metrics.NewRecorder(time.Now())
	u := &User{ID: 1, Token: "tok"}
	ctx := context.Background()
	if err := u.Connect(ctx, wsURL, rec, true); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer u.Close()

	conv := UserConversation{ID: 3, Kind: "group", Recipients: []int64{2}}
	if err := u.Send(ctx, conv, rec); err != nil {
		t.Fatalf("Send: %v", err)
	}

	eventually(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.receipts) == 2
	})
	f.mu.Lock()
	if f.auth != "Bearer tok" {
		t.Fatalf("Authorization = %q", f.auth)
	}
	types := []string{f.receipts[0].Type, f.receipts[1].Type}
	var p protocol.ReceiptPayload
	_ = json.Unmarshal(f.receipts[1].Data, &p)
	f.mu.Unlock()
	if types[0] != protocol.TypeMessageDelivered || types[1] != protocol.TypeMessageRead {
		t.Fatalf("recibos = %v", types)
	}
	if p.ConversationID != 3 || p.UpToSequence != 4 {
		t.Fatalf("recibo inesperado: %+v", p)
	}

	s := rec.Finalize()
	if s.Sent != 1 || s.Acked != 1 || s.Disconnects != 0 {
		t.Fatalf("resumo inesperado: %+v", s)
	}
}

func TestUserWithoutReceipts(t *testing.T) {
	f := &fakeWS{}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	wsURL, _ := WSURL(srv.URL)

	rec := metrics.NewRecorder(time.Now())
	u := &User{ID: 1, Token: "tok"}
	if err := u.Connect(context.Background(), wsURL, rec, false); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	_ = u.Send(context.Background(), UserConversation{ID: 3, Kind: "direct", Recipients: []int64{2}}, rec)
	eventually(t, func() bool { return rec.Finalize().Acked == 1 })
	time.Sleep(50 * time.Millisecond)
	u.Close()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.receipts) != 0 {
		t.Fatalf("recibos enviados com receipts=false: %d", len(f.receipts))
	}
	if d := rec.Finalize().Disconnects; d != 0 {
		t.Fatalf("Close contou desconexão: %d", d)
	}
}

func TestUserServerShutdown(t *testing.T) {
	f := &fakeWS{shutdown: true}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	wsURL, _ := WSURL(srv.URL)

	rec := metrics.NewRecorder(time.Now())
	u := &User{ID: 1, Token: "tok"}
	if err := u.Connect(context.Background(), wsURL, rec, true); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer u.Close()
	eventually(t, func() bool { return rec.Finalize().Disconnects == 1 })
	time.Sleep(100 * time.Millisecond)
	if d := rec.Finalize().Disconnects; d != 1 {
		t.Fatalf("desconexões = %d, esperado 1", d)
	}
}

// burstWS manda n message.new seguidas na mesma conversa (de outro
// remetente) e guarda os recibos que chegam.
func burstWS(t *testing.T, n int, f *fakeWS) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		go func() {
			for i := 1; i <= n; i++ {
				_ = writeEnvelope(ctx, conn, protocol.TypeMessageNew, protocol.Message{ID: int64(i), ConversationID: 3, Sequence: int64(i), SenderID: 99, ClientID: "x"})
			}
		}()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var env protocol.Envelope
			_ = json.Unmarshal(data, &env)
			f.mu.Lock()
			f.receipts = append(f.receipts, env)
			f.mu.Unlock()
		}
	}
}

// Recibos são cumulativos: o cliente agrupa e manda só o maior sequence, em
// vez de escrever dois frames por mensagem dentro do leitor.
func TestReceiptsCoalesced(t *testing.T) {
	f := &fakeWS{}
	srv := httptest.NewServer(burstWS(t, 200, f))
	defer srv.Close()
	wsURL, _ := WSURL(srv.URL)

	rec := metrics.NewRecorder(time.Now())
	u := &User{ID: 1, Token: "tok"}
	if err := u.Connect(context.Background(), wsURL, rec, true); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer u.Close()

	lastRead := func() int64 {
		f.mu.Lock()
		defer f.mu.Unlock()
		var last int64
		for _, env := range f.receipts {
			if env.Type == protocol.TypeMessageRead {
				var p protocol.ReceiptPayload
				_ = json.Unmarshal(env.Data, &p)
				last = max(last, p.UpToSequence)
			}
		}
		return last
	}
	eventually(t, func() bool { return lastRead() == 200 })
	f.mu.Lock()
	frames := len(f.receipts)
	f.mu.Unlock()
	if frames >= 400 {
		t.Fatalf("%d frames de recibo para 200 mensagens; esperado agrupamento", frames)
	}
}

// Um contexto já cancelado não pode derrubar a conexão nem registrar envio.
func TestSendWithCanceledContext(t *testing.T) {
	f := &fakeWS{}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	wsURL, _ := WSURL(srv.URL)

	rec := metrics.NewRecorder(time.Now())
	u := &User{ID: 1, Token: "tok"}
	if err := u.Connect(context.Background(), wsURL, rec, false); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer u.Close()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	conv := UserConversation{ID: 3, Kind: "direct", Recipients: []int64{2}}
	for i := 0; i < 20; i++ {
		if err := u.Send(canceled, conv, rec); err == nil {
			t.Fatal("Send com contexto cancelado deveria falhar")
		}
	}
	if err := u.Send(context.Background(), conv, rec); err != nil {
		t.Fatalf("conexão caiu após envios cancelados: %v", err)
	}
	eventually(t, func() bool { return rec.Finalize().Acked == 1 })
	if s := rec.Finalize(); s.Sent != 1 || s.Disconnects != 0 {
		t.Fatalf("resumo inesperado: %+v", s)
	}
}
