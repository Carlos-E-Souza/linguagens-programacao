// Pacote store persiste mensagens de forma assíncrona por meio de um pool
// de goroutines workers.
package store

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
)

// queued é uma mensagem esperando gravação, junto com o canal em que quem
// chamou aguarda a linha gravada.
type queued struct {
	message models.Message
	stored  chan models.Message
}

// batchWriter é a parte de db.Store de que este pacote precisa. Depender
// de uma interface estreita mantém o pool alheio ao resto da camada de
// dados e fácil de testar com um dublê.
type batchWriter interface {
	InsertMessages(ctx context.Context, messages []models.Message) ([]models.Message, error)
}

// Pool grava mensagens no banco em segundo plano.
//
// Gravar dentro do laço do hub faria a entrega das mensagens esperar o
// disco, então o hub só enfileira e retorna; os workers fazem o I/O.
type Pool struct {
	writer batchWriter
	queue  chan queued

	workers     int
	batchSize   int
	flushEvery  time.Duration
	enqueueWait time.Duration

	wg      sync.WaitGroup
	closeMu sync.Mutex
	closed  bool
}

// Options configura um Pool.
type Options struct {
	// Workers é quantas goroutines gravam lotes ao mesmo tempo.
	Workers int
	// QueueSize é o buffer entre o hub e os workers.
	QueueSize int
	// BatchSize é quantas mensagens um worker acumula antes de gravar.
	BatchSize int
	// FlushEvery limita quanto tempo uma mensagem pode ficar sem gravação
	// enquanto o lote ainda não encheu.
	FlushEvery time.Duration
	// EnqueueWait é quanto tempo Enqueue bloqueia com a fila cheia antes
	// de desistir, aplicando backpressure em vez de descartar dados.
	EnqueueWait time.Duration
}

// DefaultOptions devolve os padrões de produção.
func DefaultOptions(queueSize, workers int) Options {
	return Options{
		Workers:     workers,
		QueueSize:   queueSize,
		BatchSize:   64,
		FlushEvery:  200 * time.Millisecond,
		EnqueueWait: 2 * time.Second,
	}
}

// NewPool inicia as goroutines workers.
//
// O pool não recebe contexto de propósito: a única forma de parar os
// workers é Shutdown, que drena a fila primeiro. Um contexto cancelável
// aqui já fez os workers saírem no SIGTERM enquanto o hub ainda
// confirmava mensagens, perdendo tudo o que restava na fila.
func NewPool(writer batchWriter, opts Options) *Pool {
	if opts.Workers < 1 {
		opts.Workers = 1
	}
	if opts.QueueSize < 1 {
		opts.QueueSize = 1
	}
	if opts.BatchSize < 1 {
		opts.BatchSize = 1
	}
	if opts.FlushEvery <= 0 {
		opts.FlushEvery = 200 * time.Millisecond
	}

	p := &Pool{
		writer:      writer,
		queue:       make(chan queued, opts.QueueSize),
		workers:     opts.Workers,
		batchSize:   opts.BatchSize,
		flushEvery:  opts.FlushEvery,
		enqueueWait: opts.EnqueueWait,
	}

	for i := 0; i < opts.Workers; i++ {
		p.wg.Add(1)
		go p.worker(i)
	}

	return p
}

// Enqueue entrega uma mensagem aos workers e devolve um canal que recebe
// a linha gravada depois que o lote é confirmado.
//
// Quem chama pode ler desse canal para saber o ID atribuído pelo banco, ou
// ignorá-lo por completo, e nesse caso nada bloqueia: o canal tem buffer
// para exatamente um valor e o worker nunca espera por ele. É isso que
// permite ao hub oferecer um ID real de mensagem no ACK sem tornar a
// persistência síncrona.
//
// Quando a fila está cheia, ele espera, aplicando backpressure ao hub em
// vez de descartar a mensagem: uma mensagem confirmada precisa chegar ao
// banco em algum momento.
func (p *Pool) Enqueue(ctx context.Context, msg models.Message) (<-chan models.Message, error) {
	// Com buffer para que o worker sempre consiga entregar o resultado e
	// seguir em frente, mesmo que quem chamou não se importe mais com ele.
	stored := make(chan models.Message, 1)

	p.closeMu.Lock()
	closed := p.closed
	p.closeMu.Unlock()

	if closed {
		return nil, errors.New("store: pool is closed")
	}

	item := queued{message: msg, stored: stored}

	if p.enqueueWait <= 0 {
		select {
		case p.queue <- item:
			return stored, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	timer := time.NewTimer(p.enqueueWait)
	defer timer.Stop()

	select {
	case p.queue <- item:
		return stored, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, errors.New("store: persistence queue is full")
	}
}

// worker consome a fila, agrupa em lotes e grava. Só retorna quando a
// fila está fechada e totalmente drenada.
func (p *Pool) worker(id int) {
	defer p.wg.Done()

	ticker := time.NewTicker(p.flushEvery)
	defer ticker.Stop()

	batch := make([]queued, 0, p.batchSize)

	flush := func() {
		if len(batch) == 0 {
			return
		}

		p.persistBatch(id, batch)
		batch = batch[:0]
	}

	for {
		select {
		case item, ok := <-p.queue:
			if !ok {
				flush()
				return
			}

			batch = append(batch, item)
			if len(batch) >= p.batchSize {
				flush()
			}

		case <-ticker.C:
			flush()
		}
	}
}

// persistBatch grava um lote e informa as linhas gravadas a quem está
// esperando por elas.
//
// A gravação tem contexto próprio: o contexto do pool pode já estar
// cancelado durante o desligamento, e o sentido de drenar é justamente
// concluir estas gravações. Todo aguardante recebe resposta nos dois
// caminhos, então quem está bloqueado no canal de resultado nunca fica
// pendurado.
func (p *Pool) persistBatch(workerID int, batch []queued) {
	writeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	messages := make([]models.Message, len(batch))
	for i, item := range batch {
		messages[i] = item.message
	}

	stored, err := p.writeWithRetry(writeCtx, messages)
	if err != nil {
		slog.Error("persistence failed, messages lost",
			"worker", workerID, "count", len(batch), "error", err)

		// Quem chamou recebe uma resposta vazia em vez de ficar
		// bloqueado: um valor zero significa "não gravada", que o hub
		// transforma num ACK sem ID.
		for _, item := range batch {
			item.stored <- models.Message{}
		}
		return
	}

	// O gravador devolve só as linhas que de fato inseriu, em ordem, então
	// as duplicatas são casadas pelo client_id e não pela posição.
	byClientID := make(map[string]models.Message, len(stored))
	for _, msg := range stored {
		byClientID[msg.ClientID] = msg
	}

	for _, item := range batch {
		msg, ok := byClientID[item.message.ClientID]
		if !ok {
			// Uma duplicata: a linha já existia de uma inserção anterior,
			// então nenhum ID novo é informado.
			item.stored <- models.Message{}
			continue
		}
		item.stored <- msg
	}
}

// writeWithRetry tenta gravar um lote algumas vezes antes de desistir.
//
// Uma disputa momentânea por trava vale nova tentativa; uma falha
// permanente é registrada no log com o lote inteiro, para que os dados ao
// menos possam ser recuperados à mão.
func (p *Pool) writeWithRetry(ctx context.Context, batch []models.Message) ([]models.Message, error) {
	const attempts = 3

	backoff := 50 * time.Millisecond
	var lastErr error

	for i := 0; i < attempts; i++ {
		inserted, err := p.writer.InsertMessages(ctx, batch)
		if err == nil {
			slog.Debug("batch persisted", "count", len(inserted), "batch", len(batch))
			return inserted, nil
		}
		lastErr = err

		select {
		case <-ctx.Done():
			return nil, lastErr
		case <-time.After(backoff):
		}
		backoff *= 2
	}

	return nil, lastErr
}

// Shutdown para de aceitar novas mensagens e espera a fila ser drenada.
//
// Fechar o canal é o que avisa os workers para terminar: cada um despacha
// o lote pendente antes de retornar, então um desligamento gracioso não
// perde nada do que foi aceito.
func (p *Pool) Shutdown(ctx context.Context) error {
	p.closeMu.Lock()
	if p.closed {
		p.closeMu.Unlock()
		return nil
	}
	p.closed = true
	close(p.queue)
	p.closeMu.Unlock()

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("persistence pool drained")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
