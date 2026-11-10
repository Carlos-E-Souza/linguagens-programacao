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

// ErrNotFound is returned when a lookup matches no row. Handlers map it
// to 404, so callers compare against this sentinel rather than against
// driver-specific error values.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a write violates a uniqueness rule, such
// as registering a username that already exists.
var ErrConflict = errors.New("conflict")

// userColumns is the projection shared by every user query, so the scan
// order below stays in step with the SELECT list.
const userColumns = `id, username, display_name, password_hash, created_at`

// CreateUser inserts a new account and returns it with the assigned ID.
//
// The UNIQUE constraint on username is what actually enforces
// uniqueness: checking with a SELECT first would race against a
// concurrent registration.
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

// GetUserByID looks an account up by primary key.
func (s *Store) GetUserByID(ctx context.Context, id int64) (models.User, error) {
	query := `SELECT ` + userColumns + ` FROM users WHERE id = ?`
	return scanUser(s.db.QueryRowContext(ctx, query, id))
}

// GetUserByUsername looks an account up by its unique username. Used by
// the login handler, which needs the password hash.
func (s *Store) GetUserByUsername(ctx context.Context, username string) (models.User, error) {
	query := `SELECT ` + userColumns + ` FROM users WHERE username = ?`
	return scanUser(s.db.QueryRowContext(ctx, query, username))
}

// SearchUsers finds accounts whose username or display name contains the
// term, excluding the caller so they cannot start a conversation with
// themselves.
//
// LIMIT is parameterised rather than interpolated: the value comes from
// a request and string-building SQL is how injection bugs start.
func (s *Store) SearchUsers(ctx context.Context, term string, excludeID int64, limit int) ([]models.User, error) {
	// LIKE treats % and _ as wildcards, so they are escaped to keep the
	// search literal.
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

// rowScanner is satisfied by both *sql.Row and *sql.Rows, which lets one
// scan helper serve single-row and multi-row queries alike.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanUser reads one user row and maps sql.ErrNoRows onto ErrNotFound so
// callers never need to import database/sql to handle a missing row.
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

// escapeLike neutralises the LIKE wildcards in a user-supplied term. The
// backslash is escaped first, otherwise it would double-escape the
// characters added afterwards.
func escapeLike(term string) string {
	term = strings.ReplaceAll(term, `\`, `\\`)
	term = strings.ReplaceAll(term, "%", `\%`)
	term = strings.ReplaceAll(term, "_", `\_`)
	return term
}
