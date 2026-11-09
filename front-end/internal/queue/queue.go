package queue

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/api"
)

// Store é onde a fila persiste as mensagens pendentes (o cache SQLite na aplicação), para que elas sobrevivam ao
// fechamento do app.
type Store interface {
	// EnqueueSend grava a mensagem como pending e a coloca na fila, atomicamente.
	EnqueueSend(ctx context.Context, msg api.Message) error
	// RemoveSend tira a mensagem da fila.
	RemoveSend(ctx context.Context, clientID string) error
	// PendingSends devolve a fila na ordem de enfileiramento.
	PendingSends(ctx context.Context) ([]api.Message, error)
}

// Sender envia uma mensagem ao servidor (message.send). Devolve erro se não for possível agora (ex.: sem conexão);
// a mensagem continua na fila e é tentada de novo depois.
type Sender func(msg api.Message) error

// Options ajusta os tempos da fila. Valores zero usam os padrões.
type Options struct {
	AckTimeout    time.Duration // tempo sem ACK até reenviar (padrão 10s)
	CheckInterval time.Duration // frequência da verificação de reenvio (padrão 2s)
}

// Queue guarda as mensagens enviadas até o ACK do servidor e as (re)envia na ordem original. Reenviar é seguro:
// o servidor descarta duplicatas pelo client_id e devolve o mesmo ACK.
type Queue struct {
	store Store
	send  Sender
	opts  Options
	now   func() time.Time

	// mu serializa os envios (mantém a ordem) e protege sentAt.
	mu sync.Mutex
	// sentAt guarda quando cada mensagem foi enviada pela última vez nesta conexão.
	sentAt map[string]time.Time
}

// New cria a fila.
func New(store Store, send Sender, opts Options) *Queue {
	if opts.AckTimeout == 0 {
		opts.AckTimeout = 10 * time.Second
	}
	if opts.CheckInterval == 0 {
		opts.CheckInterval = 2 * time.Second
	}
	return &Queue{store: store, send: send, opts: opts, now: time.Now, sentAt: map[string]time.Time{}}
}

// Add persiste a mensagem na fila e tenta enviar tudo o que ainda não foi enviado, na ordem.
func (q *Queue) Add(ctx context.Context, msg api.Message) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.store.EnqueueSend(ctx, msg); err != nil {
		return err
	}
	q.flush(ctx, false)
	return nil
}

// Ack remove a mensagem confirmada pelo servidor.
func (q *Queue) Ack(ctx context.Context, clientID string) error {
	return q.remove(ctx, clientID)
}

// Fail remove a mensagem recusada definitivamente pelo servidor (ex.: não é mais membro da conversa).
func (q *Queue) Fail(ctx context.Context, clientID string) error {
	return q.remove(ctx, clientID)
}

func (q *Queue) remove(ctx context.Context, clientID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.sentAt, clientID)
	return q.store.RemoveSend(ctx, clientID)
}

// ResendAll reenvia a fila inteira na ordem original. Chamada após cada (re)conexão: o que foi enviado pela conexão
// anterior pode não ter chegado.
func (q *Queue) ResendAll(ctx context.Context) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sentAt = map[string]time.Time{}
	q.flush(ctx, false)
}

// RetryDue reenvia as mensagens sem ACK há mais de AckTimeout e as que ainda não foram enviadas.
func (q *Queue) RetryDue(ctx context.Context) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.flush(ctx, true)
}

// Run verifica periodicamente os reenvios até ctx ser cancelado.
func (q *Queue) Run(ctx context.Context) {
	ticker := time.NewTicker(q.opts.CheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			q.RetryDue(ctx)
		case <-ctx.Done():
			return
		}
	}
}

// flush envia, na ordem da fila, as mensagens nunca enviadas (e, com overdue, as sem ACK há mais de AckTimeout).
// Para na primeira falha, para que uma mensagem posterior não passe à frente de uma anterior. Exige q.mu.
func (q *Queue) flush(ctx context.Context, overdue bool) {
	pending, err := q.store.PendingSends(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("fila: ler pendentes: %v", err)
		}
		return
	}
	now := q.now()
	for _, msg := range pending {
		last, sent := q.sentAt[msg.ClientID]
		if sent && !(overdue && now.Sub(last) >= q.opts.AckTimeout) {
			continue
		}
		if err := q.send(msg); err != nil {
			return
		}
		q.sentAt[msg.ClientID] = now
	}
}
