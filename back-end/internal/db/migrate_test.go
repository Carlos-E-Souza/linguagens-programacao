package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// newTestDB opens a throwaway database in the test's own temporary
// directory. t.TempDir() is removed automatically when the test ends, so
// every test starts from an empty schema.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.db")
	handle, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() failed: %v", err)
	}
	t.Cleanup(func() { handle.Close() })

	return handle
}

// TestMigrateCreatesSchema checks the migration actually produces the
// tables the rest of the server depends on.
func TestMigrateCreatesSchema(t *testing.T) {
	ctx := context.Background()
	handle := newTestDB(t)

	if err := Migrate(ctx, handle); err != nil {
		t.Fatalf("Migrate() failed: %v", err)
	}

	want := []string{"users", "conversations", "conversation_members", "messages", "schema_version"}
	for _, table := range want {
		var name string
		err := handle.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %q missing after migration: %v", table, err)
		}
	}
}

// TestMigrateIsIdempotent is the property that matters in production:
// restarting the server must not try to recreate existing tables, which
// would fail with "table already exists".
func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	handle := newTestDB(t)

	if err := Migrate(ctx, handle); err != nil {
		t.Fatalf("first Migrate() failed: %v", err)
	}
	if err := Migrate(ctx, handle); err != nil {
		t.Fatalf("second Migrate() failed (not idempotent): %v", err)
	}

	var count int
	if err := handle.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_version`).Scan(&count); err != nil {
		t.Fatalf("count schema_version: %v", err)
	}
	if got, want := count, 1; got != want {
		t.Errorf("schema_version rows: got %d, want %d", got, want)
	}
}

// TestOpenEnablesForeignKeys guards the pragma: without it, the
// REFERENCES clauses in the schema would be decorative and we could
// insert a message for a conversation that does not exist.
func TestOpenEnablesForeignKeys(t *testing.T) {
	ctx := context.Background()
	handle := newTestDB(t)

	if err := Migrate(ctx, handle); err != nil {
		t.Fatalf("Migrate() failed: %v", err)
	}

	_, err := handle.ExecContext(ctx, `
		INSERT INTO messages (conversation_id, sequence, sender_id, client_id, body, created_at)
		VALUES (999, 1, 999, 'client-1', 'orphan', CURRENT_TIMESTAMP)`)
	if err == nil {
		t.Fatal("expected a foreign key violation, but the insert succeeded")
	}
}

// TestOpenEnablesWAL checks the journal mode, since a silent fall back
// to the default would reintroduce reader/writer blocking under load.
func TestOpenEnablesWAL(t *testing.T) {
	handle := newTestDB(t)

	var mode string
	if err := handle.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if got, want := mode, "wal"; got != want {
		t.Errorf("journal_mode: got %q, want %q", got, want)
	}
}

// TestMigrationFilesAreOrdered documents the naming contract: the
// lexical sort of the embedded files is the order they get applied in.
func TestMigrationFilesAreOrdered(t *testing.T) {
	files, err := migrationFiles()
	if err != nil {
		t.Fatalf("migrationFiles() failed: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no embedded migration files found")
	}

	for i := 1; i < len(files); i++ {
		if files[i-1] >= files[i] {
			t.Errorf("migrations not sorted: %q before %q", files[i-1], files[i])
		}
	}
}
