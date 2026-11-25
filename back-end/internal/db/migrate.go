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

// migrationFS embute os arquivos SQL no binário. Embutir em vez de ler do
// disco em execução faz o servidor compilado carregar o próprio schema e
// impede que ele suba com um conjunto de arquivos copiado pela metade.
//
//go:embed migrations/*.sql
var migrationFS embed.FS

// Migrate aplica todas as migrações pendentes na ordem dos nomes de
// arquivo.
//
// Cada arquivo roda na própria transação e o conjunto todo é controlado
// pela tabela schema_version, então uma migração é aplicada exatamente uma
// vez mesmo se o processo cair ou duas instâncias subirem juntas.
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

// ensureVersionTable cria a tabela de controle se ela não existir.
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

// appliedVersions devolve o conjunto de migrações já registradas.
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

	// rows.Err informa uma falha que encerrou a iteração antes da hora; sem
	// essa checagem, uma leitura truncada pareceria um resultado vazio.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schema_version: %w", err)
	}

	return applied, nil
}

// migrationFiles lista os arquivos SQL embutidos em ordem lexical, e por
// isso eles levam um prefixo numérico (0001_...).
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

// versionOf transforma "0001_initial.sql" em "0001".
func versionOf(name string) string {
	return strings.TrimSuffix(strings.SplitN(name, "_", 2)[0], ".sql")
}

// applyMigration roda um arquivo e o registra, as duas coisas numa única
// transação: se o SQL falhar no meio, a linha de versão também é desfeita
// e a migração é tentada de novo na próxima inicialização.
func applyMigration(ctx context.Context, handle *sql.DB, name, version string) error {
	content, err := migrationFS.ReadFile("migrations/" + name)
	if err != nil {
		return fmt.Errorf("read migration %s: %w", name, err)
	}

	tx, err := handle.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	// Rollback não faz nada depois de um Commit bem-sucedido, então isto é
	// seguro tanto no caminho de sucesso quanto no de falha.
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
