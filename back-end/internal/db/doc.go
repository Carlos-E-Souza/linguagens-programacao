// Package db opens the SQLite connection, applies the embedded SQL
// migrations and exposes the repositories that read and write the
// chat data.
//
// Open configures the connection for concurrent use (WAL journal mode,
// foreign keys enabled and a busy timeout) and sets a connection limit
// suited to SQLite. Migrate embeds migrations/*.sql and applies only the
// pending ones inside a transaction, tracking them in the schema_version
// table.
//
// The repositories (users, conversations, members and messages) are the
// only code allowed to touch SQL. They all take a context.Context as
// their first argument and never hold state beyond the connection
// handle, which keeps them safe to share between the REST handlers and
// the persistence workers.
package db
