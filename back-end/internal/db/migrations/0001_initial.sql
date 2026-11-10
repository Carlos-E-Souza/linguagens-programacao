-- 0001_initial.sql — initial schema for the real-time chat server.
--
-- Naming convention (team decision): every table, column and index name
-- is in English, exactly like the Go code, the HTTP/JSON payloads and
-- the WebSocket envelope. There is a single language across the whole
-- data path, so a field keeps the same name from the SQL row up to the
-- JavaScript object.

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
  name          TEXT,                       -- groups only
  created_by    INTEGER NOT NULL REFERENCES users(id),
  last_sequence INTEGER NOT NULL DEFAULT 0,
  created_at    TIMESTAMP NOT NULL
);

CREATE TABLE conversation_members (
  conversation_id INTEGER NOT NULL REFERENCES conversations(id),
  user_id         INTEGER NOT NULL REFERENCES users(id),
  role            TEXT NOT NULL CHECK (role IN ('admin', 'member')),
  last_delivered  INTEGER NOT NULL DEFAULT 0,  -- highest sequence delivered to this member
  last_read       INTEGER NOT NULL DEFAULT 0,  -- highest sequence read by this member
  joined_at       TIMESTAMP NOT NULL,
  PRIMARY KEY (conversation_id, user_id)
);

CREATE TABLE messages (
  id              INTEGER PRIMARY KEY,
  conversation_id INTEGER NOT NULL REFERENCES conversations(id),
  sequence        INTEGER NOT NULL,
  sender_id       INTEGER NOT NULL REFERENCES users(id),
  client_id       TEXT NOT NULL,               -- UUID generated on the client; prevents duplicates
  body            TEXT NOT NULL,
  created_at      TIMESTAMP NOT NULL,
  UNIQUE (conversation_id, sequence),
  UNIQUE (sender_id, client_id)
);

-- Indexes backing the two hottest read paths: paginating a
-- conversation's history by sequence, and listing the conversations a
-- user belongs to.
CREATE INDEX idx_messages_conversation_sequence ON messages (conversation_id, sequence);
CREATE INDEX idx_conversation_members_user ON conversation_members (user_id);
