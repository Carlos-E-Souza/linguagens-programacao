package hub

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/coder/websocket"
)

// Client é uma conexão WebSocket aberta pertencente a um usuário.
//
// Um usuário pode ter várias conexões ao mesmo tempo (duas janelas, um
// celular e um notebook), e por isso o hub as indexa pelo ID do usuário e
// guarda um conjunto por usuário em vez de uma única entrada.
type Client struct {
	// userID e conn são definidos uma vez na construção e depois só
	// lidos, então não precisam de sincronização.
	userID int64
	conn   *websocket.Conn

	// send é a fila de saída. O hub escreve nela; a goroutine de escrita a
	// drena. Tem buffer para que uma lentidão momentânea no socket não
	// trave o laço do hub.
	send chan []byte

	// cancel derruba as duas goroutines da conexão. Chamá-lo é como
	// qualquer parte do sistema pede para a conexão parar.
	cancel context.CancelFunc

	// ctx é o contexto da própria conexão, cancelado pelo cancel acima. Ele
	// é guardado para que o hub verifique se a conexão já se foi antes de
	// bloquear num envio pelo canal.
	ctx context.Context

	// sendMu protege a flag closed e o próprio envio. Sem ele, um trySend
	// rodando junto com close enviaria num canal fechado, o que causa
	// panic. Fechar o canal é o que avisa a goroutine de escrita para
	// drenar e parar, então ele não pode simplesmente ficar aberto.
	sendMu sync.Mutex
	closed bool

	// closeCode e closeReason são o status com que a conexão deve ser
	// fechada, escolhido por quem fechou o cliente primeiro. Protegidos
	// por sendMu.
	closeCode   websocket.StatusCode
	closeReason string
}

// newClient monta um cliente com seu próprio contexto cancelável.
//
// O contexto devolvido não deriva do contexto da requisição, de
// propósito: a requisição HTTP termina assim que o handler retorna,
// enquanto a conexão precisa durar além dela.
func newClient(userID int64, conn *websocket.Conn, queueSize int) (*Client, context.Context) {
	ctx, cancel := context.WithCancel(context.Background())

	c := &Client{
		userID: userID,
		conn:   conn,
		send:   make(chan []byte, queueSize),
		cancel: cancel,
		ctx:    ctx,
	}

	return c, ctx
}

// context devolve o contexto da própria conexão.
func (c *Client) context() context.Context {
	return c.ctx
}

// UserID devolve o dono desta conexão.
func (c *Client) UserID() int64 {
	return c.userID
}

// trySend enfileira um frame sem bloquear.
//
// Devolve false quando a fila está cheia, o que o hub trata como cliente
// lento: derrubar a conexão é melhor do que deixar um consumidor travado
// bloquear a entrega para todos os outros.
func (c *Client) trySend(frame []byte) bool {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	if c.closed {
		return false
	}

	select {
	case c.send <- frame:
		return true
	default:
		return false
	}
}

// close libera os recursos do cliente com status de fechamento normal.
// Pode ser chamado mais de uma vez, o que importa porque várias goroutines
// podem concluir ao mesmo tempo que a conexão acabou.
func (c *Client) close() {
	c.closeWith(websocket.StatusNormalClosure, "connection closed")
}

// closeWith libera os recursos do cliente e registra o status WebSocket
// com que a conexão deve ser fechada. Só a primeira chamada vale: um
// cliente lento fechado com StatusPolicyViolation mantém esse código
// mesmo que o caminho normal de unregister chame close depois.
func (c *Client) closeWith(code websocket.StatusCode, reason string) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	if c.closed {
		return
	}
	c.closed = true
	c.closeCode = code
	c.closeReason = reason

	// Cancelar antes de fechar deixa desistir quem estiver bloqueado no
	// contexto; o fechamento então deixa a goroutine de escrita drenar e
	// sair.
	c.cancel()
	close(c.send)
}

// CloseStatus devolve o status e o motivo com que a conexão deve ser
// fechada. Um cliente que nunca foi fechado pelo hub informa fechamento
// normal.
func (c *Client) CloseStatus() (websocket.StatusCode, string) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	if !c.closed {
		return websocket.StatusNormalClosure, "connection closed"
	}
	return c.closeCode, c.closeReason
}

// encodeFrame serializa um envelope nos bytes que o escritor envia.
func encodeFrame(env any) ([]byte, error) {
	return json.Marshal(env)
}

// logClientError registra uma falha da conexão junto com o usuário dela.
func logClientError(userID int64, msg string, err error) {
	slog.Debug(msg, "user_id", userID, "error", err)
}

// Read lê um frame da conexão.
//
// Só a goroutine de leitura chama isto; coder/websocket permite um leitor
// por vez.
func (c *Client) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	return c.conn.Read(ctx)
}

// Write envia um frame.
//
// Só a goroutine de escrita chama isto, o que satisfaz a regra de escritor
// único do coder/websocket sem mutex.
func (c *Client) Write(ctx context.Context, frame []byte) error {
	return c.conn.Write(ctx, websocket.MessageText, frame)
}

// Next devolve o próximo frame de saída enfileirado, bloqueando até haver
// um ou a conexão ser fechada.
//
// O segundo valor é false quando a fila está fechada e drenada, e é assim
// que a goroutine de escrita sabe que deve parar. Drenar em vez de abortar
// importa: frames já enfileirados (um erro final, um aviso de
// desligamento) ainda chegam ao cliente.
func (c *Client) Next(ctx context.Context) ([]byte, bool) {
	select {
	case frame, ok := <-c.send:
		return frame, ok
	case <-ctx.Done():
		return nil, false
	}
}

// Ping envia um ping WebSocket e espera o pong correspondente.
//
// É chamado só pela goroutine de escrita da conexão, então não disputa com
// as escritas normais de frames: coder/websocket permite um único escritor
// por vez, e o heartbeat e a fila de saída compartilham essa goroutine.
//
// Um ping que falha é como se detecta uma conexão meio aberta. O TCP
// sozinho não percebe um par que sumiu sem fechar (um notebook que
// hibernou, uma rede que caiu), então sem isto a conexão e suas duas
// goroutines ficariam penduradas indefinidamente.
func (c *Client) Ping(ctx context.Context) error {
	return c.conn.Ping(ctx)
}
