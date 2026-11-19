-- +goose Up
CREATE TABLE chat_meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
) WITHOUT ROWID;

CREATE TABLE chat_streams (
	stream  TEXT PRIMARY KEY,
	latest  INTEGER NOT NULL,
	pruned  INTEGER NOT NULL DEFAULT 0,
	dropped INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;

CREATE TABLE chat_records (
	stream   TEXT    NOT NULL,
	seq      INTEGER NOT NULL,
	event_id TEXT,
	name     TEXT    NOT NULL,
	at_ns    INTEGER NOT NULL,
	size     INTEGER NOT NULL,
	data     BLOB,
	digest   BLOB    NOT NULL,
	PRIMARY KEY (stream, seq)
) WITHOUT ROWID;

CREATE UNIQUE INDEX chat_records_event_id ON chat_records (event_id) WHERE event_id IS NOT NULL;

CREATE TABLE chat_conversations (
	id               TEXT PRIMARY KEY,
	agent            TEXT    NOT NULL,
	scope            TEXT    NOT NULL DEFAULT '',
	owner            TEXT    NOT NULL DEFAULT '',
	kind             TEXT    NOT NULL DEFAULT '',
	generation       INTEGER NOT NULL,
	runner_ref       TEXT    NOT NULL DEFAULT '',
	active_run       TEXT    NOT NULL DEFAULT '',
	lease_holder     TEXT    NOT NULL DEFAULT '',
	lease_expires_ns INTEGER NOT NULL DEFAULT 0,
	closed           INTEGER NOT NULL DEFAULT 0,
	successor        TEXT    NOT NULL DEFAULT '',
	created_ns       INTEGER NOT NULL
) WITHOUT ROWID;

-- One current conversation per agent.
CREATE UNIQUE INDEX chat_conversations_current ON chat_conversations (agent) WHERE closed = 0;

CREATE TABLE chat_attachments (
	conversation_id TEXT    NOT NULL,
	id              TEXT    NOT NULL,
	role            TEXT    NOT NULL,
	stream          TEXT    NOT NULL,
	generation      INTEGER NOT NULL,
	created_ns      INTEGER NOT NULL,
	PRIMARY KEY (conversation_id, id)
) WITHOUT ROWID;

-- +goose Down
DROP TABLE chat_attachments;
DROP TABLE chat_conversations;
DROP TABLE chat_records;
DROP TABLE chat_streams;
DROP TABLE chat_meta;
