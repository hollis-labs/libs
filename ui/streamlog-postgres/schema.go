package streamlogpostgres

const schemaVersion = "1"

// schemaSQL is formatted with the quoted schema name.
const schemaSQL = `
CREATE SCHEMA IF NOT EXISTS %[1]s;

CREATE TABLE IF NOT EXISTS %[1]s.log_meta (
	key   text PRIMARY KEY,
	value text NOT NULL
);

CREATE TABLE IF NOT EXISTS %[1]s.streams (
	stream  text PRIMARY KEY,
	latest  bigint NOT NULL,
	pruned  bigint NOT NULL DEFAULT 0,
	dropped bigint NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS %[1]s.records (
	stream         text        NOT NULL,
	seq            bigint      NOT NULL,
	event_id       text,
	kind           text        NOT NULL,
	at             timestamptz NOT NULL,
	tier           smallint    NOT NULL DEFAULT 0,
	size           integer     NOT NULL,
	data           bytea,
	payload        jsonb,
	digest         bytea       NOT NULL,
	payload_digest text,
	class          text        NOT NULL DEFAULT '',
	policy_id      text        NOT NULL DEFAULT '',
	rolled         boolean     NOT NULL DEFAULT false,
	outcome        text        NOT NULL DEFAULT '',
	category       text        NOT NULL DEFAULT '',
	severity       text        NOT NULL DEFAULT '',
	sensitivity    text        NOT NULL DEFAULT '',
	pii            boolean     NOT NULL DEFAULT false,
	visibility     text        NOT NULL DEFAULT '',
	generation     bigint      NOT NULL DEFAULT 0,
	project_id     text        NOT NULL DEFAULT '',
	workstream_id  text        NOT NULL DEFAULT '',
	task_id        text        NOT NULL DEFAULT '',
	session_id     text        NOT NULL DEFAULT '',
	agent_urn      text        NOT NULL DEFAULT '',
	run_id         text        NOT NULL DEFAULT '',
	PRIMARY KEY (stream, seq)
);

CREATE UNIQUE INDEX IF NOT EXISTS records_event_id  ON %[1]s.records (event_id) WHERE event_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS records_project     ON %[1]s.records (stream, project_id, seq)    WHERE project_id <> '';
CREATE INDEX IF NOT EXISTS records_workstream  ON %[1]s.records (stream, workstream_id, seq) WHERE workstream_id <> '';
CREATE INDEX IF NOT EXISTS records_task        ON %[1]s.records (stream, task_id, seq)       WHERE task_id <> '';
CREATE INDEX IF NOT EXISTS records_session     ON %[1]s.records (stream, session_id, seq)    WHERE session_id <> '';
CREATE INDEX IF NOT EXISTS records_agent       ON %[1]s.records (stream, agent_urn, seq)     WHERE agent_urn <> '';
CREATE INDEX IF NOT EXISTS records_kind        ON %[1]s.records (stream, kind, seq);
CREATE INDEX IF NOT EXISTS records_at          ON %[1]s.records (stream, at);
CREATE INDEX IF NOT EXISTS records_unrolled    ON %[1]s.records (stream, class, seq) WHERE NOT rolled;
CREATE INDEX IF NOT EXISTS records_unclassified ON %[1]s.records (stream, seq) WHERE class = '';

CREATE TABLE IF NOT EXISTS %[1]s.rollups (
	stream        text        NOT NULL,
	class         text        NOT NULL,
	bucket_ns     bigint      NOT NULL,
	bucket_start  timestamptz NOT NULL,
	kind          text        NOT NULL,
	category      text        NOT NULL DEFAULT '',
	severity      text        NOT NULL DEFAULT '',
	project_id    text        NOT NULL DEFAULT '',
	workstream_id text        NOT NULL DEFAULT '',
	agent_urn     text        NOT NULL DEFAULT '',
	outcome       text        NOT NULL DEFAULT '',
	count         bigint      NOT NULL,
	first_seq     bigint      NOT NULL,
	last_seq      bigint      NOT NULL,
	PRIMARY KEY (stream, class, bucket_ns, bucket_start, kind, category, severity, project_id, workstream_id, agent_urn, outcome)
);
`
