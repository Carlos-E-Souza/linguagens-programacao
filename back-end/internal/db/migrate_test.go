package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// newTestDB abre um banco descartável no diretório temporário do próprio
// teste. t.TempDir() é removido automaticamente ao fim do teste, então
// todo teste começa com o schema vazio.
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

// TestMigrateCreatesSchema confere que a migração de fato cria as tabelas
// de que o resto do servidor depende.
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

// TestMigrateIsIdempotent é a propriedade que importa em produção:
// reiniciar o servidor não pode tentar recriar tabelas existentes, o que
// falharia com "table already exists".
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

// TestOpenEnablesForeignKeys protege o pragma: sem ele, as cláusulas
// REFERENCES do schema seriam decorativas e daria para inserir uma
// mensagem numa conversa que não existe.
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

// TestOpenEnablesWAL confere o modo de journal, já que voltar em silêncio
// ao padrão reintroduziria o bloqueio entre leitores e escritores sob
// carga.
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

// TestMigrationFilesAreOrdered documenta o contrato de nomes: a ordem
// lexical dos arquivos embutidos é a ordem em que são aplicados.
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
