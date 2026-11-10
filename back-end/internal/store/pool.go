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
	queue  chan models.Message

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
		queue:       make(chan models.Message, opts.QueueSize),
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

// Enqueue hands a message to the workers.
//
// When the queue is full it waits, applying backpressure to the hub
// rather than discarding the message: a message that was acknowledged
// must eventually reach the database.
func (p *Pool) Enqueue(ctx context.Context, msg models.Message) error {
	p.closeMu.Lock()
	closed := p.closed
	p.closeMu.Unlock()

	if closed {
		return errors.New("store: pool is closed")
	}

	if p.enqueueWait <= 0 {
		select {
		case p.queue <- msg:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	timer := time.NewTimer(p.enqueueWait)
	defer timer.Stop()

	select {
	case p.queue <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("store: persistence queue is full")
	}
}

// worker consumes the queue, batches and writes.
func (p *Pool) worker(ctx context.Context, id int) {
	defer p.wg.Done()

	ticker := time.NewTicker(p.flushEvery)
	defer ticker.Stop()

	batch := make([]models.Message, 0, p.batchSize)

	flush := func() {
		if len(batch) == 0 {
			return
		}

		// The write gets its own context: the pool's context may already
		// be cancelled during shutdown, and the whole point of draining
		// is to finish these writes.
		writeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := p.writeWithRetry(writeCtx, batch); err != nil {
			slog.Error("persistence failed, messages lost",
				"worker", id, "count", len(batch), "error", err)
		}

		batch = batch[:0]
	}

	for {
		select {
		case msg, ok := <-p.queue:
			if !ok {
				flush()
				return
			}

			batch = append(batch, msg)
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

// writeWithRetry attempts a batch a few times before giving up.
//
// A transient lock contention is worth retrying; a permanent failure is
// logged with the full batch so the data is at least recoverable by hand.
func (p *Pool) writeWithRetry(ctx context.Context, batch []models.Message) error {
	const attempts = 3

	backoff := 50 * time.Millisecond
	var lastErr error

	for i := 0; i < attempts; i++ {
		inserted, err := p.writer.InsertMessages(ctx, batch)
		if err == nil {
			slog.Debug("batch persisted", "count", len(inserted), "batch", len(batch))
			return nil
		}
		lastErr = err

		select {
		case <-ctx.Done():
			return lastErr
		case <-time.After(backoff):
		}
		backoff *= 2
	}

	return lastErr
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
