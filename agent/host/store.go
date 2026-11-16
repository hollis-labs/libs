package host

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hollis-labs/libs/llm/core/llmtypes"

	_ "modernc.org/sqlite" // the driver OpenSQLite uses
)

// Store keeps the conversations: one running thread per owner, its messages, the runs and
// the standing permissions. SQLite (OpenSQLite, NewSQLite) is the one in the package.
type Store interface {
	// MainThread is the owner's running thread, created on first use.
	MainThread(ctx context.Context, owner string) (string, error)
	// ArchiveMain puts the running thread away; the next message starts a new one.
	ArchiveMain(ctx context.Context, owner string) error
	// Append adds a message to the end of a thread.
	Append(ctx context.Context, thread, runID, role string, blocks []llmtypes.ContentBlock, meta json.RawMessage) (Message, error)
	// Messages are a thread's messages, oldest first.
	Messages(ctx context.Context, thread string) ([]Message, error)

	StartRun(ctx context.Context, id, thread, owner, model string) error
	FinishRun(ctx context.Context, id string, end RunEnd) error
	// RunOwner is whose run this is ("" when there is no such run).
	RunOwner(ctx context.Context, id string) (string, error)
	// LastPrefix is how large the thread's last model request was, in tokens (0: unknown).
	LastPrefix(ctx context.Context, thread string) (int, error)

	// Granted says whether the owner has allowed the tool for good ("always").
	Granted(ctx context.Context, owner, tool string) (bool, error)
	Grant(ctx context.Context, owner, tool string) error

	// Summary is the thread's compaction summary and the seq of the last message it
	// covers ("" and 0 when the thread has not been compacted).
	Summary(ctx context.Context, thread string) (string, int, error)
	SetSummary(ctx context.Context, thread, text string, upto int) error
}

// Message is one stored message. Blocks are what the model is sent; Meta is the
// interface's ({text, context?, auto?} on a person's message).
type Message struct {
	ID        string                  `json:"id"`
	Seq       int                     `json:"seq"`
	Role      string                  `json:"role"`
	Blocks    []llmtypes.ContentBlock `json:"blocks"`
	Meta      json.RawMessage         `json:"meta"`
	RunID     string                  `json:"run_id,omitempty"`
	CreatedAt string                  `json:"created_at"`
}

// RunEnd is how a run ended.
type RunEnd struct {
	Status       string // finished, cancelled, error
	Reason       string // the finish reason, or the error
	InputTokens  int
	OutputTokens int
	PrefixTokens int // the size of its last model request, with the answer
}

// SQLite is the package's Store: its own tables (agenthost_*), in a file of its own or in
// the application's database.
type SQLite struct {
	db  *sql.DB
	own bool
}

// The schema moves forward by number; the database remembers how far in
// agenthost_schema. Add to the end; never edit one that has shipped.
var migrations = []string{
	`CREATE TABLE agenthost_threads (
		id TEXT PRIMARY KEY,
		owner TEXT NOT NULL,
		archived_at TEXT NOT NULL DEFAULT '',
		summary TEXT NOT NULL DEFAULT '',
		summary_upto INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	CREATE INDEX agenthost_threads_owner ON agenthost_threads(owner, archived_at);
	CREATE TABLE agenthost_messages (
		id TEXT PRIMARY KEY,
		thread_id TEXT NOT NULL REFERENCES agenthost_threads(id) ON DELETE CASCADE,
		seq INTEGER NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		meta TEXT NOT NULL DEFAULT '{}',
		run_id TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		UNIQUE(thread_id, seq)
	);
	CREATE TABLE agenthost_runs (
		id TEXT PRIMARY KEY,
		thread_id TEXT NOT NULL REFERENCES agenthost_threads(id) ON DELETE CASCADE,
		owner TEXT NOT NULL,
		status TEXT NOT NULL,
		model TEXT NOT NULL DEFAULT '',
		reason TEXT NOT NULL DEFAULT '',
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		prefix_tokens INTEGER NOT NULL DEFAULT 0,
		started_at TEXT NOT NULL,
		finished_at TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX agenthost_runs_thread ON agenthost_runs(thread_id, started_at);
	CREATE TABLE agenthost_grants (
		owner TEXT NOT NULL,
		tool TEXT NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY(owner, tool)
	);`,
}

// OpenSQLite opens (creating or upgrading) a SQLite file of the host's own.
func OpenSQLite(ctx context.Context, path string) (*SQLite, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer at a time; the writes are small
	s, err := NewSQLite(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	s.own = true
	return s, nil
}

// NewSQLite puts the host's tables in a database the application already has open (with
// the modernc.org/sqlite driver or another SQLite driver). Close does not close db.
func NewSQLite(ctx context.Context, db *sql.DB) (*SQLite, error) {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS agenthost_schema (version INTEGER NOT NULL)`); err != nil {
		return nil, err
	}
	var have int
	err := db.QueryRowContext(ctx, `SELECT version FROM agenthost_schema`).Scan(&have)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = db.ExecContext(ctx, `INSERT INTO agenthost_schema(version) VALUES(0)`)
	}
	if err != nil {
		return nil, err
	}
	if have > len(migrations) {
		return nil, fmt.Errorf("agenthost: the database was written by a newer agenthost (schema %d, this build knows %d)", have, len(migrations))
	}
	for i := have; i < len(migrations); i++ {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, migrations[i]); err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE agenthost_schema SET version=?`, i+1)
		}
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		if err != nil {
			return nil, fmt.Errorf("agenthost: schema step %d: %w", i+1, err)
		}
	}
	// A run that was going when the application last stopped did not finish.
	_, _ = db.ExecContext(ctx, `UPDATE agenthost_runs SET status='error', reason='The host stopped while this was running.', finished_at=? WHERE status='running'`, now())
	return &SQLite{db: db}, nil
}

// Close closes the file OpenSQLite opened; for NewSQLite it does nothing.
func (s *SQLite) Close() error {
	if s.own {
		return s.db.Close()
	}
	return nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func newID(prefix string) string { return prefix + "_" + rand.Text()[:20] }

func (s *SQLite) MainThread(ctx context.Context, owner string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM agenthost_threads WHERE owner=? AND archived_at='' ORDER BY created_at DESC LIMIT 1`, owner).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id = newID("thr")
	t := now()
	_, err = s.db.ExecContext(ctx, `INSERT INTO agenthost_threads(id, owner, created_at, updated_at) VALUES(?,?,?,?)`, id, owner, t, t)
	return id, err
}

func (s *SQLite) ArchiveMain(ctx context.Context, owner string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agenthost_threads SET archived_at=? WHERE owner=? AND archived_at=''`, now(), owner)
	return err
}

func (s *SQLite) Append(ctx context.Context, thread, runID, role string, blocks []llmtypes.ContentBlock, meta json.RawMessage) (Message, error) {
	content, err := json.Marshal(blocks)
	if err != nil {
		return Message{}, err
	}
	if len(meta) == 0 {
		meta = json.RawMessage("{}")
	}
	m := Message{ID: newID("msg"), Role: role, Blocks: blocks, Meta: meta, RunID: runID, CreatedAt: now()}
	err = s.db.QueryRowContext(ctx, `INSERT INTO agenthost_messages(id, thread_id, seq, role, content, meta, run_id, created_at)
		VALUES(?,?,(SELECT COALESCE(MAX(seq),0)+1 FROM agenthost_messages WHERE thread_id=?),?,?,?,?,?) RETURNING seq`,
		m.ID, thread, thread, role, string(content), string(meta), runID, m.CreatedAt).Scan(&m.Seq)
	if err != nil {
		return Message{}, err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE agenthost_threads SET updated_at=? WHERE id=?`, m.CreatedAt, thread)
	return m, nil
}

func (s *SQLite) Messages(ctx context.Context, thread string) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, seq, role, content, meta, run_id, created_at FROM agenthost_messages WHERE thread_id=? ORDER BY seq`, thread)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		var content, meta string
		if err := rows.Scan(&m.ID, &m.Seq, &m.Role, &content, &meta, &m.RunID, &m.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(content), &m.Blocks); err != nil {
			return nil, fmt.Errorf("agenthost: message %s is not readable: %w", m.ID, err)
		}
		m.Meta = json.RawMessage(meta)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *SQLite) StartRun(ctx context.Context, id, thread, owner, model string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO agenthost_runs(id, thread_id, owner, status, model, started_at) VALUES(?,?,?,'running',?,?)`, id, thread, owner, model, now())
	return err
}

func (s *SQLite) FinishRun(ctx context.Context, id string, e RunEnd) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agenthost_runs SET status=?, reason=?, input_tokens=?, output_tokens=?, prefix_tokens=?, finished_at=? WHERE id=?`,
		e.Status, e.Reason, e.InputTokens, e.OutputTokens, e.PrefixTokens, now(), id)
	return err
}

func (s *SQLite) RunOwner(ctx context.Context, id string) (string, error) {
	var owner string
	err := s.db.QueryRowContext(ctx, `SELECT owner FROM agenthost_runs WHERE id=?`, id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return owner, err
}

func (s *SQLite) LastPrefix(ctx context.Context, thread string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT prefix_tokens FROM agenthost_runs WHERE thread_id=? AND prefix_tokens>0 ORDER BY started_at DESC LIMIT 1`, thread).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

func (s *SQLite) Granted(ctx context.Context, owner, tool string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agenthost_grants WHERE owner=? AND tool=?`, owner, tool).Scan(&n)
	return n > 0, err
}

func (s *SQLite) Grant(ctx context.Context, owner, tool string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO agenthost_grants(owner, tool, created_at) VALUES(?,?,?)`, owner, tool, now())
	return err
}

// Grants are the tools the owner has allowed for good, for an application's settings page.
func (s *SQLite) Grants(ctx context.Context, owner string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tool FROM agenthost_grants WHERE owner=? ORDER BY tool`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Revoke takes a standing permission back.
func (s *SQLite) Revoke(ctx context.Context, owner, tool string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM agenthost_grants WHERE owner=? AND tool=?`, owner, tool)
	return err
}

func (s *SQLite) Summary(ctx context.Context, thread string) (string, int, error) {
	var text string
	var upto int
	err := s.db.QueryRowContext(ctx, `SELECT summary, summary_upto FROM agenthost_threads WHERE id=?`, thread).Scan(&text, &upto)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil
	}
	return text, upto, err
}

func (s *SQLite) SetSummary(ctx context.Context, thread, text string, upto int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agenthost_threads SET summary=?, summary_upto=? WHERE id=?`, text, upto, thread)
	return err
}
