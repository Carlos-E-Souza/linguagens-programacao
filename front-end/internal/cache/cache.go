package cache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Carlos-E-Souza/linguagens-programacao/front-end/internal/api"

	_ "modernc.org/sqlite"
)

// appDirName é a pasta do cliente dentro de os.UserConfigDir().
const appDirName = "chat-client"

// lastUserFile guarda o nome do último usuário logado, para RestoreSession saber qual banco abrir.
const lastUserFile = "last-user"

// schema cria as tabelas do cache local. IF NOT EXISTS mantém a abertura idempotente.
const schema = `
CREATE TABLE IF NOT EXISTS session (
  id           INTEGER PRIMARY KEY CHECK (id = 1),
  token        TEXT NOT NULL,
  user_id      INTEGER NOT NULL,
  username     TEXT NOT NULL,
  display_name TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS conversations (
  id            INTEGER PRIMARY KEY,
  type          TEXT NOT NULL,
  name          TEXT NOT NULL DEFAULT '',
  created_by    INTEGER NOT NULL DEFAULT 0,
  last_sequence INTEGER NOT NULL DEFAULT 0,
  unread_count  INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  last_activity TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS members (
  conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  user_id         INTEGER NOT NULL,
  username        TEXT NOT NULL,
  display_name    TEXT NOT NULL,
  role            TEXT NOT NULL,
  last_delivered  INTEGER NOT NULL DEFAULT 0,
  last_read       INTEGER NOT NULL DEFAULT 0,
  online          INTEGER NOT NULL DEFAULT 0,
  joined_at       TEXT NOT NULL,
  PRIMARY KEY (conversation_id, user_id)
);

CREATE TABLE IF NOT EXISTS messages (
  local_id        INTEGER PRIMARY KEY,
  id              INTEGER,
  conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  sequence        INTEGER,
  sender_id       INTEGER NOT NULL,
  client_id       TEXT NOT NULL,
  body            TEXT NOT NULL,
  created_at      TEXT NOT NULL,
  status          TEXT NOT NULL,
  UNIQUE (sender_id, client_id)
);
CREATE INDEX IF NOT EXISTS idx_messages_conversation_sequence ON messages (conversation_id, sequence);

CREATE TABLE IF NOT EXISTS send_queue (
  client_id       TEXT PRIMARY KEY,
  conversation_id INTEGER NOT NULL,
  body            TEXT NOT NULL,
  enqueued_at     TEXT NOT NULL
);

-- Sequência até a qual a reconexão já recuperou todo o histórico novo.
CREATE TABLE IF NOT EXISTS sync_cursors (
  conversation_id INTEGER PRIMARY KEY REFERENCES conversations(id) ON DELETE CASCADE,
  last_sequence   INTEGER NOT NULL DEFAULT 0
);
`

// statusRank ordena os status para que uma atualização nunca faça uma mensagem "voltar" (ex.: read → sent).
// failed fica fora da escala: só é gravado explicitamente por UpdateStatus.
const statusRank = `CASE %s WHEN 'pending' THEN 0 WHEN 'sent' THEN 1 WHEN 'delivered' THEN 2 WHEN 'read' THEN 3 ELSE -1 END`

// Cache é o banco SQLite local de um usuário.
type Cache struct {
	db       *sql.DB
	changeMu sync.Mutex
	revision uint64
}

// Dir devolve a pasta de dados do cliente, criando-a se preciso.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("cache: pasta de configuração: %w", err)
	}
	dir := filepath.Join(base, appDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("cache: criar %s: %w", dir, err)
	}
	return dir, nil
}

var unsafeChars = regexp.MustCompile(`[^a-z0-9_-]`)

// PathFor devolve o caminho do banco do usuário dentro de dir.
func PathFor(dir, username string) string {
	name := unsafeChars.ReplaceAllString(strings.ToLower(username), "_")
	return filepath.Join(dir, name+".db")
}

// SaveLastUser registra o último usuário logado.
func SaveLastUser(dir, username string) error {
	return os.WriteFile(filepath.Join(dir, lastUserFile), []byte(username), 0o600)
}

// LoadLastUser devolve o último usuário logado, ou "" se não houver.
func LoadLastUser(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, lastUserFile))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return strings.TrimSpace(string(data)), err
}

// ClearLastUser apaga o registro do último usuário.
func ClearLastUser(dir string) error {
	err := os.Remove(filepath.Join(dir, lastUserFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Open abre (ou cria) o banco em path e aplica o esquema.
func Open(path string) (*Cache, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("cache: abrir %s: %w", path, err)
	}
	// Uma conexão basta para o cliente e evita SQLITE_BUSY entre goroutines.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("cache: aplicar esquema: %w", err)
	}
	// Ao atualizar um cache antigo, congela o ponto de partida antes de qualquer
	// atualização REST alterar a última mensagem da conversa.
	if _, err := db.Exec(`INSERT INTO sync_cursors (conversation_id, last_sequence)
		SELECT c.id, COALESCE((SELECT MAX(m.sequence) FROM messages m WHERE m.conversation_id = c.id), 0)
		FROM conversations c WHERE NOT EXISTS (
			SELECT 1 FROM sync_cursors s WHERE s.conversation_id = c.id)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("cache: iniciar cursores de sincronização: %w", err)
	}
	return &Cache{db: db}, nil
}

// Close fecha o banco.
func (c *Cache) Close() error {
	return c.db.Close()
}

// SaveSession guarda o token e o usuário logado.
func (c *Cache) SaveSession(ctx context.Context, token string, user api.User) error {
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO session (id, token, user_id, username, display_name) VALUES (1, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET token = excluded.token, user_id = excluded.user_id,
		  username = excluded.username, display_name = excluded.display_name`,
		token, user.ID, user.Username, user.DisplayName)
	return wrap("salvar sessão", err)
}

// LoadSession devolve a sessão salva. ok é false quando não há sessão.
func (c *Cache) LoadSession(ctx context.Context) (token string, user api.User, ok bool, err error) {
	err = c.db.QueryRowContext(ctx, `SELECT token, user_id, username, display_name FROM session WHERE id = 1`).
		Scan(&token, &user.ID, &user.Username, &user.DisplayName)
	if errors.Is(err, sql.ErrNoRows) {
		return "", api.User{}, false, nil
	}
	if err != nil {
		return "", api.User{}, false, wrap("ler sessão", err)
	}
	return token, user, true, nil
}

// ClearSession apaga o token salvo.
func (c *Cache) ClearSession(ctx context.Context) error {
	_, err := c.db.ExecContext(ctx, `DELETE FROM session`)
	return wrap("apagar sessão", err)
}

// SaveConversations grava (upsert) as conversas com seus membros e a última mensagem.
func (c *Cache) SaveConversations(ctx context.Context, conversations []api.Conversation) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		for _, conv := range conversations {
			if err := saveConversation(ctx, tx, conv); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReplaceConversations grava as conversas e apaga as que não estão na lista (o usuário saiu ou foi removido).
// Devolve os IDs apagados.
func (c *Cache) ReplaceConversations(ctx context.Context, conversations []api.Conversation) ([]int64, error) {
	var removed []int64
	err := c.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		removed, err = replaceConversationsTx(ctx, tx, conversations)
		return err
	})
	return removed, err
}

// Revision devolve a versão das alterações locais que afetam as conversas.
// Uma resposta REST iniciada nessa versão não pode substituir eventos mais novos.
func (c *Cache) Revision() uint64 {
	c.changeMu.Lock()
	defer c.changeMu.Unlock()
	return c.revision
}

// ReplaceConversationsIfCurrent só grava a resposta REST quando nenhum evento
// mudou o cache desde o início da requisição. A verificação e a gravação são
// protegidas pelo mesmo mutex para não haver uma alteração entre as duas.
func (c *Cache) ReplaceConversationsIfCurrent(ctx context.Context, conversations []api.Conversation,
	revision uint64) (removed []int64, applied bool, err error) {
	c.changeMu.Lock()
	defer c.changeMu.Unlock()
	if c.revision != revision {
		return nil, false, nil
	}
	err = c.runTx(ctx, func(tx *sql.Tx) error {
		var txErr error
		removed, txErr = replaceConversationsTx(ctx, tx, conversations)
		return txErr
	})
	if err != nil {
		return nil, false, err
	}
	c.revision++
	return removed, true, nil
}

func replaceConversationsTx(ctx context.Context, tx *sql.Tx, conversations []api.Conversation) ([]int64, error) {
	keep := make(map[int64]bool, len(conversations))
	for _, conv := range conversations {
		keep[conv.ID] = true
		if err := saveConversation(ctx, tx, conv); err != nil {
			return nil, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM conversations`)
	if err != nil {
		return nil, err
	}
	var removed []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		if !keep[id] {
			removed = append(removed, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range removed {
		if _, err := tx.ExecContext(ctx, `DELETE FROM conversations WHERE id = ?`, id); err != nil {
			return nil, err
		}
	}
	return removed, nil
}

func saveConversation(ctx context.Context, tx *sql.Tx, conv api.Conversation) error {
	activity := conv.CreatedAt
	if conv.LastMessage != nil && conv.LastMessage.CreatedAt.After(activity) {
		activity = conv.LastMessage.CreatedAt
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO conversations (id, type, name, created_by, last_sequence, unread_count, created_at, last_activity)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET type = excluded.type, name = excluded.name, created_by = excluded.created_by,
		  last_sequence = MAX(conversations.last_sequence, excluded.last_sequence),
		  unread_count = excluded.unread_count, created_at = excluded.created_at,
		  last_activity = MAX(conversations.last_activity, excluded.last_activity)`,
		conv.ID, conv.Type, conv.Name, conv.CreatedBy, conv.LastSequence, conv.UnreadCount,
		formatTime(conv.CreatedAt), formatTime(activity))
	if err != nil {
		return fmt.Errorf("salvar conversa %d: %w", conv.ID, err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM members WHERE conversation_id = ?`, conv.ID); err != nil {
		return err
	}
	for _, m := range conv.Members {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO members (conversation_id, user_id, username, display_name, role, last_delivered, last_read,
			  online, joined_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			conv.ID, m.UserID, m.Username, m.DisplayName, m.Role, m.LastDelivered, m.LastRead, m.Online,
			formatTime(m.JoinedAt))
		if err != nil {
			return fmt.Errorf("salvar membro %d da conversa %d: %w", m.UserID, conv.ID, err)
		}
	}

	if conv.LastMessage != nil {
		msg := *conv.LastMessage
		msg.ConversationID = conv.ID
		if err := saveMessage(ctx, tx, msg); err != nil {
			return err
		}
	}
	// As posições dos membros vieram do servidor: o status das mensagens próprias pode ter avançado.
	_, err = refreshOwnStatuses(ctx, tx, conv.ID)
	return err
}

// conversationColumns são as colunas lidas por scanConversation.
const conversationColumns = `id, type, name, created_by, last_sequence, unread_count, created_at`

func scanConversation(row interface{ Scan(...any) error }) (api.Conversation, error) {
	var conv api.Conversation
	var createdAt string
	err := row.Scan(&conv.ID, &conv.Type, &conv.Name, &conv.CreatedBy, &conv.LastSequence, &conv.UnreadCount, &createdAt)
	conv.CreatedAt = parseTime(createdAt)
	return conv, err
}

// ListConversations devolve as conversas do cache, da atividade mais recente para a mais antiga.
func (c *Cache) ListConversations(ctx context.Context) ([]api.Conversation, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT `+conversationColumns+`
		FROM conversations ORDER BY last_activity DESC, id DESC`)
	if err != nil {
		return nil, wrap("listar conversas", err)
	}
	var conversations []api.Conversation
	for rows.Next() {
		conv, err := scanConversation(rows)
		if err != nil {
			rows.Close()
			return nil, wrap("ler conversa", err)
		}
		conversations = append(conversations, conv)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, wrap("listar conversas", err)
	}

	// Membros e última mensagem são lidos depois de fechar rows: há uma única conexão.
	for i := range conversations {
		if err := c.fillConversation(ctx, &conversations[i]); err != nil {
			return nil, err
		}
	}
	return conversations, nil
}

// GetConversation devolve uma conversa do cache, com membros e última mensagem. ok é false se ela não existir.
func (c *Cache) GetConversation(ctx context.Context, id int64) (conv api.Conversation, ok bool, err error) {
	conv, err = scanConversation(c.db.QueryRowContext(ctx, `SELECT `+conversationColumns+`
		FROM conversations WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return api.Conversation{}, false, nil
	}
	if err != nil {
		return api.Conversation{}, false, wrap("ler conversa", err)
	}
	if err := c.fillConversation(ctx, &conv); err != nil {
		return api.Conversation{}, false, err
	}
	return conv, true, nil
}

// fillConversation completa a conversa com os membros e a última mensagem.
func (c *Cache) fillConversation(ctx context.Context, conv *api.Conversation) error {
	var err error
	if conv.Members, err = c.listMembers(ctx, conv.ID); err != nil {
		return err
	}
	last, err := c.ListMessages(ctx, conv.ID, 0, 1)
	if err != nil {
		return err
	}
	conv.LastMessage = nil
	if len(last) == 1 {
		conv.LastMessage = &last[0]
	}
	return nil
}

func (c *Cache) listMembers(ctx context.Context, conversationID int64) ([]api.Member, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT user_id, username, display_name, role, last_delivered, last_read, online, joined_at
		FROM members WHERE conversation_id = ? ORDER BY display_name`, conversationID)
	if err != nil {
		return nil, wrap("listar membros", err)
	}
	defer rows.Close()
	members := []api.Member{}
	for rows.Next() {
		var m api.Member
		var joinedAt string
		if err := rows.Scan(&m.UserID, &m.Username, &m.DisplayName, &m.Role, &m.LastDelivered, &m.LastRead,
			&m.Online, &joinedAt); err != nil {
			return nil, wrap("ler membro", err)
		}
		m.JoinedAt = parseTime(joinedAt)
		members = append(members, m)
	}
	return members, wrap("listar membros", rows.Err())
}

// SaveMessages grava (upsert) mensagens. A chave é (sender_id, client_id), a mesma do servidor; assim o ACK ou o
// histórico do servidor completam a mensagem pendente criada localmente, sem duplicar.
func (c *Cache) SaveMessages(ctx context.Context, messages []api.Message) error {
	return c.inTx(ctx, func(tx *sql.Tx) error {
		touched := make(map[int64]bool)
		for _, msg := range messages {
			if err := saveMessage(ctx, tx, msg); err != nil {
				return err
			}
			touched[msg.ConversationID] = true
		}
		// O histórico do servidor chega como sent; as posições dos membros dizem se já foi entregue ou lido.
		for id := range touched {
			if _, err := refreshOwnStatuses(ctx, tx, id); err != nil {
				return err
			}
		}
		return nil
	})
}

func saveMessage(ctx context.Context, tx *sql.Tx, msg api.Message) error {
	if msg.ClientID == "" {
		msg.ClientID = fmt.Sprintf("server-%d", msg.ID)
	}
	if msg.Status == "" {
		msg.Status = api.StatusSent
	}
	var id, sequence any
	if msg.ID > 0 {
		id = msg.ID
	}
	if msg.Sequence > 0 {
		sequence = msg.Sequence
	}
	query := fmt.Sprintf(`
		INSERT INTO messages (id, conversation_id, sequence, sender_id, client_id, body, created_at, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (sender_id, client_id) DO UPDATE SET
		  id = COALESCE(excluded.id, messages.id),
		  sequence = COALESCE(excluded.sequence, messages.sequence),
		  created_at = CASE WHEN excluded.sequence IS NOT NULL THEN excluded.created_at ELSE messages.created_at END,
		  status = CASE WHEN %s > %s THEN excluded.status ELSE messages.status END`,
		fmt.Sprintf(statusRank, "excluded.status"), fmt.Sprintf(statusRank, "messages.status"))
	_, err := tx.ExecContext(ctx, query, id, msg.ConversationID, sequence, msg.SenderID, msg.ClientID, msg.Body,
		formatTime(msg.CreatedAt), msg.Status)
	if err != nil {
		return fmt.Errorf("salvar mensagem %s: %w", msg.ClientID, err)
	}
	if msg.Sequence > 0 {
		_, err = tx.ExecContext(ctx, `
			UPDATE conversations SET last_sequence = MAX(last_sequence, ?), last_activity = MAX(last_activity, ?)
			WHERE id = ?`, msg.Sequence, formatTime(msg.CreatedAt), msg.ConversationID)
	}
	return err
}

// ListMessages devolve até limit mensagens com sequência menor que before, em ordem crescente de sequência.
// before = 0 pede as mais recentes, incluindo as pendentes (ainda sem sequência), que ficam no fim.
func (c *Cache) ListMessages(ctx context.Context, conversationID, before int64, limit int) ([]api.Message, error) {
	query := `SELECT id, conversation_id, sequence, sender_id, client_id, body, created_at, status
		FROM messages WHERE conversation_id = ?`
	args := []any{conversationID}
	if before > 0 {
		query += ` AND sequence < ?`
		args = append(args, before)
	}
	// Pendentes (sequence NULL) contam como as mais recentes.
	query += ` ORDER BY sequence IS NULL DESC, sequence DESC, local_id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrap("listar mensagens", err)
	}
	defer rows.Close()
	var messages []api.Message
	for rows.Next() {
		var msg api.Message
		var id, sequence sql.NullInt64
		var createdAt string
		if err := rows.Scan(&id, &msg.ConversationID, &sequence, &msg.SenderID, &msg.ClientID, &msg.Body,
			&createdAt, &msg.Status); err != nil {
			return nil, wrap("ler mensagem", err)
		}
		msg.ID, msg.Sequence, msg.CreatedAt = id.Int64, sequence.Int64, parseTime(createdAt)
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap("listar mensagens", err)
	}
	// A consulta vem da mais nova para a mais antiga; a interface quer ordem crescente.
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	return messages, nil
}

// LastSequence devolve a maior sequência de mensagem guardada da conversa.
func (c *Cache) LastSequence(ctx context.Context, conversationID int64) (int64, error) {
	var seq sql.NullInt64
	err := c.db.QueryRowContext(ctx, `SELECT MAX(sequence) FROM messages WHERE conversation_id = ?`, conversationID).
		Scan(&seq)
	return seq.Int64, wrap("última sequência", err)
}

// UpdateStatus muda o status de uma mensagem identificada pelo client_id.
func (c *Cache) UpdateStatus(ctx context.Context, clientID, status string) error {
	_, err := c.db.ExecContext(ctx, `UPDATE messages SET status = ? WHERE client_id = ?`, status, clientID)
	return wrap("atualizar status", err)
}

func (c *Cache) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	c.changeMu.Lock()
	defer c.changeMu.Unlock()
	if err := c.runTx(ctx, fn); err != nil {
		return err
	}
	c.revision++
	return nil
}

func (c *Cache) runTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("iniciar transação", err)
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return wrap("transação", err)
	}
	return wrap("confirmar transação", tx.Commit())
}

func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("cache: %s: %w", op, err)
}

// formatTime grava horários em UTC com precisão fixa, para que a comparação textual (MAX, ORDER BY) seja cronológica.
func formatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
