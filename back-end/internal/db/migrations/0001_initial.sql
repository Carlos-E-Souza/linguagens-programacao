-- 0001_initial.sql — schema inicial do servidor de chat em tempo real.
--
-- Convenção de nomes (decisão do time): todo nome de tabela, coluna e
-- índice é em inglês, exatamente como no código Go, nos payloads
-- HTTP/JSON e no envelope WebSocket. Há uma única língua em todo o
-- caminho dos dados, então um campo mantém o mesmo nome da linha SQL até
-- o objeto JavaScript.

CREATE TABLE users (
  id             INTEGER PRIMARY KEY,
  username       TEXT NOT NULL UNIQUE,
  display_name   TEXT NOT NULL,
  password_hash  TEXT NOT NULL,
  created_at     TIMESTAMP NOT NULL
);

CREATE TABLE conversations (
  id            INTEGER PRIMARY KEY,
  type          TEXT NOT NULL CHECK (type IN ('direct', 'group')),
  name          TEXT,                       -- só para grupos
  created_by    INTEGER NOT NULL REFERENCES users(id),
  last_sequence INTEGER NOT NULL DEFAULT 0,
  created_at    TIMESTAMP NOT NULL
);

CREATE TABLE conversation_members (
  conversation_id INTEGER NOT NULL REFERENCES conversations(id),
  user_id         INTEGER NOT NULL REFERENCES users(id),
  role            TEXT NOT NULL CHECK (role IN ('admin', 'member')),
  last_delivered  INTEGER NOT NULL DEFAULT 0,  -- maior sequence entregue a este membro
  last_read       INTEGER NOT NULL DEFAULT 0,  -- maior sequence lida por este membro
  joined_at       TIMESTAMP NOT NULL,
  PRIMARY KEY (conversation_id, user_id)
);

CREATE TABLE messages (
  id              INTEGER PRIMARY KEY,
  conversation_id INTEGER NOT NULL REFERENCES conversations(id),
  sequence        INTEGER NOT NULL,
  sender_id       INTEGER NOT NULL REFERENCES users(id),
  client_id       TEXT NOT NULL,               -- UUID gerado no cliente; evita duplicatas
  body            TEXT NOT NULL,
  created_at      TIMESTAMP NOT NULL,
  UNIQUE (conversation_id, sequence),
  UNIQUE (sender_id, client_id)
);

-- Índices que sustentam os dois caminhos de leitura mais usados: paginar
-- o histórico de uma conversa por sequence e listar as conversas de que
-- um usuário participa.
CREATE INDEX idx_messages_conversation_sequence ON messages (conversation_id, sequence);
CREATE INDEX idx_conversation_members_user ON conversation_members (user_id);
