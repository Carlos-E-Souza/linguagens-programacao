package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
)

// migrationFS embeds the SQL files into the binary. Embedding rather than
// reading from disk at runtime means a built server carries its schema
// with it and cannot be started against a half-copied set of files.
//
//go:embed migrations/*.sql
var migrationFS embed.FS

// Migrate applies every pending migration in filename order.
//
// Each file runs inside its own transaction and the whole set is guarded
// by the schema_version table, so a migration is applied exactly once
// even if the process crashes or two instances start together.
func Migrate(ctx context.Context, handle *sql.DB) error {
	if err := ensureVersionTable(ctx, handle); err != nil {
		return err
	}

	applied, err := appliedVersions(ctx, handle)
	if err != nil {
		return err
	}

	files, err := migrationFiles()
	if err != nil {
		return err
	}

	for _, name := range files {
		version := versionOf(name)
		if _, ok := applied[version]; ok {
			continue
		}

		if err := applyMigration(ctx, handle, name, version); err != nil {
			return err
		}

		slog.Info("migration applied", "version", version, "file", name)
	}

	return nil
}

// ensureVersionTable creates the bookkeeping table if it is missing.
func ensureVersionTable(ctx context.Context, handle *sql.DB) error {
	const query = `
		CREATE TABLE IF NOT EXISTS schema_version (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`

	if _, err := handle.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("create schema_version table: %w", err)
	}
	return nil
}

// appliedVersions returns the set of migrations already recorded.
func appliedVersions(ctx context.Context, handle *sql.DB) (map[string]struct{}, error) {
	rows, err := handle.QueryContext(ctx, `SELECT version FROM schema_version`)
	if err != nil {
		return nil, fmt.Errorf("read schema_version: %w", err)
	}
	defer rows.Close()

	applied := make(map[string]struct{})
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("scan schema_version: %w", err)
		}
		applied[version] = struct{}{}
	}

	// rows.Err reports a failure that ended the iteration early; without
	// this check a truncated read would look like an empty result set.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schema_version: %w", err)
	}

	return applied, nil
}

// migrationFiles lists the embedded SQL files in lexical order, which is
// why they carry a numeric prefix (0001_...).
func migrationFiles() ([]string, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	return names, nil
}

// versionOf turns "0001_initial.sql" into "0001".
func versionOf(name string) string {
	return strings.TrimSuffix(strings.SplitN(name, "_", 2)[0], ".sql")
}

// applyMigration runs one file and records it, both in a single
// transaction: if the SQL fails halfway, the version row is rolled back
// too and the migration is retried on the next start.
func applyMigration(ctx context.Context, handle *sql.DB, name, version string) error {
	content, err := migrationFS.ReadFile("migrations/" + name)
	if err != nil {
		return fmt.Errorf("read migration %s: %w", name, err)
	}

	tx, err := handle.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	// Rollback is a no-op once Commit succeeds, so this is safe on both
	// the success and the failure path.
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, string(content)); err != nil {
		return fmt.Errorf("apply migration %s: %w", name, err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_version (version) VALUES (?)`, version); err != nil {
		return fmt.Errorf("record migration %s: %w", name, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", name, err)
	}

	return nil
}
