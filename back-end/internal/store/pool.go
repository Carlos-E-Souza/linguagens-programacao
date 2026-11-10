// Package store persists messages asynchronously through a pool of
// worker goroutines.
package store

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
)

// queued is one message waiting to be written, together with the channel
// its caller is watching for the stored row.
type queued struct {
	message models.Message
	stored  chan models.Message
}

// batchWriter is the part of db.Store this package needs. Depending on a
// narrow interface keeps the pool unaware of the rest of the data layer
// and easy to test with a fake.
type batchWriter interface {
	InsertMessages(ctx context.Context, messages []models.Message) ([]models.Message, error)
}

// Pool writes messages to the database in the background.
//
// Writing inside the hub loop would make message delivery wait on disk,
// so the hub only enqueues and returns; the workers do the I/O.
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

// Options configures a Pool.
type Options struct {
	// Workers is how many goroutines write batches concurrently.
	Workers int
	// QueueSize is the buffer between the hub and the workers.
	QueueSize int
	// BatchSize is how many messages a worker accumulates before writing.
	BatchSize int
	// FlushEvery bounds how long a message may sit unwritten while the
	// batch is not yet full.
	FlushEvery time.Duration
	// EnqueueWait is how long Enqueue blocks when the queue is full
	// before giving up, applying backpressure instead of dropping data.
	EnqueueWait time.Duration
}

// DefaultOptions returns the production defaults.
func DefaultOptions(queueSize, workers int) Options {
	return Options{
		Workers:     workers,
		QueueSize:   queueSize,
		BatchSize:   64,
		FlushEvery:  200 * time.Millisecond,
		EnqueueWait: 2 * time.Second,
	}
}

// NewPool starts the worker goroutines.
//
// The pool takes a context so that cancelling it stops the workers, but
// Shutdown remains the orderly path: it drains the queue first.
func NewPool(ctx context.Context, writer batchWriter, opts Options) *Pool {
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
		go p.worker(ctx, i)
	}

	return p
}

// Enqueue hands a message to the workers and returns a channel that
// receives the stored row once the batch has been committed.
//
// The caller may read from that channel to learn the database-assigned
// ID, or ignore it entirely, in which case nothing blocks: the channel is
// buffered with room for exactly one value and the worker never waits on
// it. This is what lets the hub offer a real message ID in the
// acknowledgement without making persistence synchronous.
//
// When the queue is full it waits, applying backpressure to the hub
// rather than discarding the message: a message that was acknowledged
// must eventually reach the database.
func (p *Pool) Enqueue(ctx context.Context, msg models.Message) (<-chan models.Message, error) {
	// Buffered so the worker can always deliver the result and move on,
	// even if the caller stopped caring about it.
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

// worker consumes the queue, batches and writes.
func (p *Pool) worker(ctx context.Context, id int) {
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

		case <-ctx.Done():
			flush()
			return
		}
	}
}

// persistBatch writes one batch and reports the stored rows back to the
// callers that are waiting for them.
//
// The write gets its own context: the pool's context may already be
// cancelled during shutdown, and the whole point of draining is to finish
// these writes. Every waiter is answered on both paths, so a caller
// blocked on the result channel is never left hanging.
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

		// The callers are told nothing rather than being left blocked:
		// a zero value means "not stored", which the hub turns into an
		// acknowledgement without an ID.
		for _, item := range batch {
			item.stored <- models.Message{}
		}
		return
	}

	// The writer returns only the rows it actually inserted, in order, so
	// duplicates are matched by client_id rather than by position.
	byClientID := make(map[string]models.Message, len(stored))
	for _, msg := range stored {
		byClientID[msg.ClientID] = msg
	}

	for _, item := range batch {
		msg, ok := byClientID[item.message.ClientID]
		if !ok {
			// A duplicate: the row already existed from an earlier
			// insert, so no new ID is reported.
			item.stored <- models.Message{}
			continue
		}
		item.stored <- msg
	}
}

// writeWithRetry attempts a batch a few times before giving up.
//
// A transient lock contention is worth retrying; a permanent failure is
// logged with the full batch so the data is at least recoverable by hand.
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

// Shutdown stops accepting new messages and waits for the queue to drain.
//
// Closing the channel is what tells the workers to finish: each one
// flushes its pending batch before returning, so a graceful shutdown
// loses nothing that was accepted.
// Shutdown stops accepting new messages and waits for the queue to drain.
//
// Closing the channel is what tells the workers to finish: each one
// flushes its pending batch before returning, so a graceful shutdown
// loses nothing that was accepted.
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
