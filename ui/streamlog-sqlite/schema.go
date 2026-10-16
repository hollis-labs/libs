package streamlogsqlite

// schemaVersion is stored in log_meta. A database with a newer version is
// refused rather than read with the wrong shape.
const schemaVersion = "1"

// The Postgres log uses the same columns (see the sibling module), so moving
// between them is a data copy. data is NULL once retention tombstones a row
// (tier 1); the envelope columns, event_id, digest and payload_digest stay.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS log_meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS streams (
	stream  TEXT PRIMARY KEY,
	latest  INTEGER NOT NULL,
	pruned  INTEGER NOT NULL DEFAULT 0,
	dropped INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS records (
	stream         TEXT    NOT NULL,
	seq            INTEGER NOT NULL,
	event_id       TEXT,
	kind           TEXT    NOT NULL,
	at_ns          INTEGER NOT NULL,
	tier           INTEGER NOT NULL DEFAULT 0,
	size           INTEGER NOT NULL,
	data           BLOB,
	digest         BLOB    NOT NULL,
	payload_digest TEXT,
	class          TEXT    NOT NULL DEFAULT '',
	policy_id      TEXT    NOT NULL DEFAULT '',
	rolled         INTEGER NOT NULL DEFAULT 0,
	outcome        TEXT    NOT NULL DEFAULT '',
	category       TEXT    NOT NULL DEFAULT '',
	severity       TEXT    NOT NULL DEFAULT '',
	sensitivity    TEXT    NOT NULL DEFAULT '',
	pii            INTEGER NOT NULL DEFAULT 0,
	visibility     TEXT    NOT NULL DEFAULT '',
	generation     INTEGER NOT NULL DEFAULT 0,
	project_id     TEXT    NOT NULL DEFAULT '',
	workstream_id  TEXT    NOT NULL DEFAULT '',
	task_id        TEXT    NOT NULL DEFAULT '',
	session_id     TEXT    NOT NULL DEFAULT '',
	agent_urn      TEXT    NOT NULL DEFAULT '',
	run_id         TEXT    NOT NULL DEFAULT '',
	PRIMARY KEY (stream, seq)
) WITHOUT ROWID;

CREATE UNIQUE INDEX IF NOT EXISTS records_event_id ON records (event_id) WHERE event_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS records_project  ON records (stream, project_id, seq)  WHERE project_id <> '';
CREATE INDEX IF NOT EXISTS records_task     ON records (stream, task_id, seq)     WHERE task_id <> '';
CREATE INDEX IF NOT EXISTS records_session  ON records (stream, session_id, seq)  WHERE session_id <> '';
CREATE INDEX IF NOT EXISTS records_workstream ON records (stream, workstream_id, seq) WHERE workstream_id <> '';
CREATE INDEX IF NOT EXISTS records_agent    ON records (stream, agent_urn, seq)    WHERE agent_urn <> '';
CREATE INDEX IF NOT EXISTS records_kind     ON records (stream, kind, seq);
CREATE INDEX IF NOT EXISTS records_at       ON records (stream, at_ns);
CREATE INDEX IF NOT EXISTS records_unrolled ON records (stream, class, seq) WHERE rolled = 0;
CREATE INDEX IF NOT EXISTS records_unclassified ON records (stream, seq) WHERE class = '';

-- Rollup buckets (retention tier 3). Filled by the retention janitor; the
-- table is part of the log schema so a log never needs a separate migration.
CREATE TABLE IF NOT EXISTS rollups (
	stream        TEXT    NOT NULL,
	class         TEXT    NOT NULL,
	bucket_ns     INTEGER NOT NULL,
	bucket_start  INTEGER NOT NULL,
	kind          TEXT    NOT NULL,
	category      TEXT    NOT NULL DEFAULT '',
	severity      TEXT    NOT NULL DEFAULT '',
	project_id    TEXT    NOT NULL DEFAULT '',
	workstream_id TEXT    NOT NULL DEFAULT '',
	agent_urn     TEXT    NOT NULL DEFAULT '',
	outcome       TEXT    NOT NULL DEFAULT '',
	count         INTEGER NOT NULL,
	first_seq     INTEGER NOT NULL,
	last_seq      INTEGER NOT NULL,
	PRIMARY KEY (stream, class, bucket_ns, bucket_start, kind, category, severity, project_id, workstream_id, agent_urn, outcome)
) WITHOUT ROWID;
`
