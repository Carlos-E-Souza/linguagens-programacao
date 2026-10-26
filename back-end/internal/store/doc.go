// Package store persists messages asynchronously through a pool of
// worker goroutines.
//
// Writing to the database inside the hub loop would make delivery wait
// on disk, so the hub only enqueues a message and returns. A fixed
// number of workers (PERSISTENCE_WORKERS) consumes that queue, batches
// the messages and writes each batch in a single transaction, which
// amortises the cost of fsync across many inserts.
//
// Enqueue applies backpressure instead of dropping data: when the queue
// is full it blocks with a timeout. Shutdown closes the queue and waits
// for the workers to drain it within the context deadline, which is how
// the graceful shutdown guarantees that every acknowledged message is
// on disk before the process exits.
//
// Ordering in the table does not matter, since the display order is
// given by the sequence column, so the workers can write in parallel
// without coordination.
package store
