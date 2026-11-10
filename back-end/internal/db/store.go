package db

import (
	"context"
	"database/sql"
	"strings"
)

// Store exposes every database operation of the server.
//
// It is a thin wrapper around *sql.DB rather than a set of free
// functions so that handlers can depend on one value. It holds no
// mutable state, so a single instance is safe to share between the REST
// handlers, the hub and the persistence workers.
type Store struct {
	db *sql.DB
}

// NewStore wraps an open database handle.
func NewStore(handle *sql.DB) *Store {
	return &Store{db: handle}
}

// DB returns the underlying handle, for the few callers that need to run
// their own transaction across several repositories.
func (s *Store) DB() *sql.DB {
	return s.db
}

// isUniqueViolation reports whether err is a UNIQUE constraint failure.
//
// modernc.org/sqlite does not export a typed error for this, so the
// check is by message. It is confined to this one function so that a
// driver change only needs a fix here.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToUpper(err.Error()), "UNIQUE CONSTRAINT FAILED")
}

// isForeignKeyViolation reports whether err is a FOREIGN KEY failure.
func isForeignKeyViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToUpper(err.Error()), "FOREIGN KEY CONSTRAINT FAILED")
}

// txFunc is the body of a transaction. Returning an error rolls back;
// returning nil commits.
type txFunc func(tx *sql.Tx) error

// withTx runs fn inside a transaction, committing on success and rolling
// back on any error or panic.
//
// The rollback is deferred unconditionally because Rollback after a
// successful Commit returns sql.ErrTxDone and does nothing, so the
// success path is unaffected.
func (s *Store) withTx(ctx context.Context, fn txFunc) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := fn(tx); err != nil {
		return err
	}

	return tx.Commit()
}
