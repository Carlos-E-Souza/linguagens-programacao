package httputil

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/auth"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/hub"
	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/ws"
)

// maxFrameBytes limita um único frame WebSocket recebido. Um cliente que
// manda mais que isso está quebrado ou é hostil; em qualquer caso a
// conexão é fechada em vez de acumular o frame.
const maxFrameBytes = 1 << 20 // 1 MiB

// writeTimeout limita a escrita de um frame, para que um par que parou de
// ler não prenda uma goroutine para sempre.
const writeTimeout = 10 * time.Second

// handleWS faz o upgrade de uma requisição autenticada e entrega a conexão
// ao hub.
//
// A autenticação acontece antes do upgrade, com o mesmo bearer token da
// API REST. O cliente é Go, então pode definir o header no handshake; isso
// evita um token na query string, que acabaria nos logs do servidor e no
// histórico do navegador.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if s.hub == nil {
		WriteError(w, http.StatusServiceUnavailable, "not_configured", "the realtime hub is not configured")
		return
	}
	if s.validator == nil {
		WriteError(w, http.StatusServiceUnavailable, "not_configured", "authentication is not configured")
		return
	}

	raw, err := auth.BearerToken(r.Header.Get("Authorization"))
	if err != nil {
		WriteError(w, http.StatusUnauthorized, "unauthorized", "missing or malformed authorization header")
		return
	}

	userID, err := s.validator.ValidateToken(raw)
	if err != nil {
		WriteError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired token")
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// O cliente desktop é um processo Wails/Go, não uma página de
		// navegador, então não envia header Origin. A checagem padrão o
		// rejeitaria.
		InsecureSkipVerify: true,
	})
	if err != nil {
		slog.Debug("websocket accept failed", "user_id", userID, "error", err)
		return
	}

	conn.SetReadLimit(maxFrameBytes)

	s.serveConnection(conn, userID)
}

// serveConnection roda os laços de leitura e escrita até um deles falhar.
//
// Os dois laços compartilham um contexto, então quando um termina o outro
// é cancelado: um leitor que falha não pode deixar um escritor bloqueado
// num socket morto, e vice-versa.
func (s *Server) serveConnection(conn *websocket.Conn, userID int64) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := s.hub.NewClient(userID, conn)
	s.hub.Register(client)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		defer cancel()

		if s.writeLoop(ctx, client) {
			// O hub encerrou a conexão (cliente lento, desligamento). O
			// fechamento acontece aqui, enquanto o laço de leitura ainda
			// está lendo, para que o par receba o frame de close com o
			// status do hub. Cancelar antes faria o Read pendente derrubar
			// o socket sem frame de close nenhum.
			code, reason := client.CloseStatus()
			_ = conn.Close(code, reason)
		}
	}()

	go func() {
		defer wg.Done()
		defer cancel()
		s.readLoop(ctx, client)
	}()

	// Espera os dois laços terminarem e garante que o hub esqueça o
	// cliente. Unregister é idempotente, então chamar duas vezes é
	// inofensivo.
	wg.Wait()

	s.hub.Unregister(client)

	// Qualquer outro encerramento (o par fechou, falha de ping ou de
	// escrita) já quebrou a conexão; libera-a sem esperar um handshake com
	// um par que talvez já tenha sumido.
	_ = conn.CloseNow()
}

// readLoop decodifica os frames e os entrega ao hub.
//
// Ele nunca mexe no estado do hub diretamente: todo frame passa pelo canal
// de entrada do hub, o que mantém o hub em uma única goroutine.
func (s *Server) readLoop(ctx context.Context, client *hub.Client) {
	for {
		msgType, data, err := client.Read(ctx)
		if err != nil {
			slog.Debug("websocket read ended", "user_id", client.UserID(), "error", err)
			return
		}
		if msgType != websocket.MessageText {
			continue
		}

		var env ws.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			slog.Debug("discarding malformed frame", "user_id", client.UserID(), "error", err)
			continue
		}
		if env.Type == "" {
			slog.Debug("discarding frame without a type", "user_id", client.UserID())
			continue
		}

		if err := s.hub.Submit(ctx, client, env, len(data)); err != nil {
			return
		}
	}
}

// writeLoop drena a fila de saída e escreve cada frame. Informa se parou
// porque o hub fechou o cliente, e não por falha de escrita ou de ping,
// ou pelo fim do lado de leitura.
//
// Um único escritor por conexão é o que torna o WebSocket seguro:
// coder/websocket permite só um escritor por vez, e serializar aqui
// permite ao hub entregar frames sem coordenação.
func (s *Server) writeLoop(ctx context.Context, client *hub.Client) (hubClosed bool) {
	// Uma goroutine repassa a fila de saída para um canal simples, para
	// que o laço abaixo possa fazer select nos frames e no timer do
	// heartbeat ao mesmo tempo. Iniciá-la uma vez — e não a cada frame —
	// mantém estável o número de goroutines e evita vazar uma goroutine
	// por mensagem.
	frames := make(chan []byte)
	go func() {
		defer close(frames)

		for {
			frame, ok := client.Next(ctx)
			if !ok {
				return
			}

			select {
			case frames <- frame:
			case <-ctx.Done():
				return
			}
		}
	}()

	ticker := time.NewTicker(s.cfg.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false

		case <-ticker.C:
			// O ping serve também de teste de vida: se o par sumiu, a
			// escrita falha, e derrubar a conexão aqui é o que libera as
			// goroutines dela e marca o usuário como offline.
			pingCtx, cancel := context.WithTimeout(ctx, s.cfg.PongTimeout)
			err := client.Ping(pingCtx)
			cancel()

			if err != nil {
				slog.Debug("websocket heartbeat failed",
					"user_id", client.UserID(), "error", err)
				return false
			}

		case frame, ok := <-frames:
			if !ok {
				// A fila foi fechada enquanto o contexto da própria
				// conexão está vivo: o hub fechou este cliente.
				return ctx.Err() == nil
			}

			writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := client.Write(writeCtx, frame)
			cancel()

			if err != nil {
				slog.Debug("websocket write ended", "user_id", client.UserID(), "error", err)
				return false
			}
		}
	}
}
