package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/back-end/internal/models"
)

// ErrNotFound é devolvido quando uma busca não encontra nenhuma linha. Os
// handlers o convertem em 404, então quem chama compara com este sentinela
// e não com valores de erro específicos do driver.
var ErrNotFound = errors.New("not found")

// ErrConflict é devolvido quando uma gravação viola uma regra de
// unicidade, como cadastrar um username que já existe.
var ErrConflict = errors.New("conflict")

// userColumns é a projeção comum a todas as consultas de usuário, para que
// a ordem do scan abaixo acompanhe a lista do SELECT.
const userColumns = `id, username, display_name, password_hash, created_at`

// CreateUser insere uma nova conta e a devolve com o ID atribuído.
//
// A restrição UNIQUE em username é o que de fato garante a unicidade:
// checar antes com um SELECT geraria corrida com um cadastro simultâneo.
func (s *Store) CreateUser(ctx context.Context, username, displayName, passwordHash string) (models.User, error) {
	const query = `
		INSERT INTO users (username, display_name, password_hash, created_at)
		VALUES (?, ?, ?, ?)`

	now := time.Now().UTC()

	result, err := s.db.ExecContext(ctx, query, username, displayName, passwordHash, now)
	if err != nil {
		if isUniqueViolation(err) {
			return models.User{}, fmt.Errorf("create user %q: %w", username, ErrConflict)
		}
		return models.User{}, fmt.Errorf("create user %q: %w", username, err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return models.User{}, fmt.Errorf("create user %q: read last insert id: %w", username, err)
	}

	return models.User{
		ID:           id,
		Username:     username,
		DisplayName:  displayName,
		PasswordHash: passwordHash,
		CreatedAt:    now,
	}, nil
}

// GetUserByID busca uma conta pela chave primária.
func (s *Store) GetUserByID(ctx context.Context, id int64) (models.User, error) {
	query := `SELECT ` + userColumns + ` FROM users WHERE id = ?`
	return scanUser(s.db.QueryRowContext(ctx, query, id))
}

// GetUserByUsername busca uma conta pelo username único. Usado pelo
// handler de login, que precisa do hash da senha.
func (s *Store) GetUserByUsername(ctx context.Context, username string) (models.User, error) {
	query := `SELECT ` + userColumns + ` FROM users WHERE username = ?`
	return scanUser(s.db.QueryRowContext(ctx, query, username))
}

// SearchUsers encontra contas cujo username ou nome de exibição contém o
// termo, excluindo quem chama, para que ninguém abra uma conversa consigo
// mesmo.
//
// LIMIT é parametrizado, e não interpolado: o valor vem de uma requisição,
// e montar SQL com strings é como começam os bugs de injeção.
func (s *Store) SearchUsers(ctx context.Context, term string, excludeID int64, limit int) ([]models.User, error) {
	// LIKE trata % e _ como curingas, então eles são escapados para manter
	// a busca literal.
	pattern := "%" + escapeLike(term) + "%"

	query := `
		SELECT ` + userColumns + `
		FROM users
		WHERE id != ?
		  AND (username LIKE ? ESCAPE '\' OR display_name LIKE ? ESCAPE '\')
		ORDER BY username
		LIMIT ?`

	rows, err := s.db.QueryContext(ctx, query, excludeID, pattern, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("search users: %w", err)
	}
	defer rows.Close()

	users := make([]models.User, 0, limit)
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search users: %w", err)
	}

	return users, nil
}

// rowScanner é satisfeita tanto por *sql.Row quanto por *sql.Rows, o que
// permite que um único helper de scan sirva consultas de uma ou de várias
// linhas.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanUser lê uma linha de usuário e converte sql.ErrNoRows em ErrNotFound,
// para que quem chama nunca precise importar database/sql para tratar uma
// linha ausente.
func scanUser(row rowScanner) (models.User, error) {
	var user models.User

	err := row.Scan(&user.ID, &user.Username, &user.DisplayName, &user.PasswordHash, &user.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return models.User{}, ErrNotFound
	}
	if err != nil {
		return models.User{}, fmt.Errorf("scan user: %w", err)
	}

	return user, nil
}

// escapeLike neutraliza os curingas do LIKE num termo vindo do usuário. A
// barra invertida é escapada primeiro, senão ela escaparia de novo os
// caracteres adicionados depois.
func escapeLike(term string) string {
	term = strings.ReplaceAll(term, `\`, `\\`)
	term = strings.ReplaceAll(term, "%", `\%`)
	term = strings.ReplaceAll(term, "_", `\_`)
	return term
}
