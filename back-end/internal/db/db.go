package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Open opens the SQLite database at path and configures it for the
// access pattern of this server: many concurrent readers and a few
// writers.
//
// The pragmas matter more than they look. SQLite defaults to a rollback
// journal and to foreign keys being off, which would both surprise a
// reader coming from another database: WAL lets readers proceed while a
// writer holds the lock, and foreign_keys=ON is what actually enforces
// the REFERENCES clauses in the schema.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	dsn := buildDSN(path)

	handle, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}

	// SQLite serialises writes, so a large pool only produces lock
	// contention and busy errors. A handful of connections keeps reads
	// concurrent without letting writers pile up.
	handle.SetMaxOpenConns(4)
	handle.SetMaxIdleConns(4)

	// Ping verifies the file is actually reachable now, rather than
	// letting the first query fail later at an unrelated call site.
	if err := handle.PingContext(ctx); err != nil {
		handle.Close()
		return nil, fmt.Errorf("ping sqlite database: %w", err)
	}

	return handle, nil
}

// buildDSN assembles the connection string with the pragmas applied to
// every new connection in the pool. Setting them in the DSN rather than
// issuing PRAGMA statements after opening is deliberate: pragmas are
// per-connection, so a statement would only configure whichever
// connection happened to be checked out at the time.
func buildDSN(path string) string {
	pragmas := url.Values{}
	pragmas.Add("_pragma", "journal_mode(WAL)")
	pragmas.Add("_pragma", "foreign_keys(ON)")
	pragmas.Add("_pragma", "busy_timeout(5000)")
	pragmas.Add("_pragma", "synchronous(NORMAL)")

	return "file:" + path + "?" + pragmas.Encode()
}
