package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	toolresult "github.com/hollis-labs/go-toolresult"
)

// Default table and scope column names (Nanite's schema).
const (
	DefaultTable       = "tool_result_cache"
	DefaultScopeColumn = "session_id"
)

// Table names the table and the scope column. The remaining columns are fixed:
// id, tool_name, tool_call_id, created_at, expires_at, byte_size,
// was_truncated and body. Zero fields take the defaults
// (tool_result_cache / session_id); Loom uses
// Table{Name: "wiki_result_cache", ScopeColumn: "caller_id"}.
type Table struct {
	Name        string
	ScopeColumn string
}

func (t Table) withDefaults() Table {
	if t.Name == "" {
		t.Name = DefaultTable
	}
	if t.ScopeColumn == "" {
		t.ScopeColumn = DefaultScopeColumn
	}
	return t
}

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

func (t Table) validate() error {
	if !identifier.MatchString(t.Name) || !identifier.MatchString(t.ScopeColumn) {
		return fmt.Errorf("sqlstore: table %q / scope column %q are not plain identifiers", t.Name, t.ScopeColumn)
	}
	return nil
}

// DDL returns idempotent CREATE TABLE and CREATE INDEX statements for t. Run
// them once when the database is created; sqlstore never migrates on its own.
// It panics if t holds an invalid identifier, like [New].
func DDL(t Table) string {
	t = t.withDefaults()
	if err := t.validate(); err != nil {
		panic(err)
	}
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %[1]s (
    id TEXT PRIMARY KEY,
    %[2]s TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    tool_call_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    byte_size INTEGER NOT NULL,
    was_truncated INTEGER NOT NULL DEFAULT 0,
    body TEXT
);
CREATE INDEX IF NOT EXISTS idx_%[1]s_%[2]s ON %[1]s(%[2]s);
CREATE INDEX IF NOT EXISTS idx_%[1]s_expires ON %[1]s(expires_at);
`, t.Name, t.ScopeColumn)
}

// Store is a database/sql-backed [toolresult.Store].
type Store struct {
	db  *sql.DB
	put string
	get string
	del string
}

var _ toolresult.Store = (*Store)(nil)

// New returns a Store over db using table t. It panics if t contains a name
// that is not a plain identifier, because the names are spliced into SQL; that
// is a programming error, not a runtime condition.
func New(db *sql.DB, t Table) *Store {
	t = t.withDefaults()
	if err := t.validate(); err != nil {
		panic(err)
	}
	// Identifiers are validated above; values always use placeholders.
	return &Store{
		db: db,
		put: fmt.Sprintf(`INSERT INTO %s (id, %s, tool_name, tool_call_id, created_at, expires_at, byte_size, was_truncated, body)
 VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?)`, t.Name, t.ScopeColumn),
		get: fmt.Sprintf(`SELECT tool_name, tool_call_id, created_at, expires_at, byte_size, body FROM %s WHERE id = ? AND %s = ?`, t.Name, t.ScopeColumn),
		del: fmt.Sprintf(`DELETE FROM %s WHERE expires_at < ?`, t.Name),
	}
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// Put inserts e. A body-less entry (BodyStored false) is stored with a NULL
// body.
func (s *Store) Put(ctx context.Context, e toolresult.Entry) error {
	body := sql.NullString{String: e.Body, Valid: e.BodyStored}
	_, err := s.db.ExecContext(ctx, s.put, e.ID, e.Scope, e.Tool, e.CallID, stamp(e.CreatedAt), stamp(e.ExpiresAt), e.ByteSize, body)
	return err
}

// Get returns the entry with that id owned by scope, or
// [toolresult.ErrNotFound].
func (s *Store) Get(ctx context.Context, scope, id string) (toolresult.Entry, error) {
	e := toolresult.Entry{ID: id, Scope: scope}
	var created, expires string
	var body sql.NullString
	err := s.db.QueryRowContext(ctx, s.get, id, scope).Scan(&e.Tool, &e.CallID, &created, &expires, &e.ByteSize, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return toolresult.Entry{}, toolresult.ErrNotFound
	}
	if err != nil {
		return toolresult.Entry{}, err
	}
	if e.CreatedAt, err = time.Parse(time.RFC3339, created); err != nil {
		return toolresult.Entry{}, fmt.Errorf("sqlstore: malformed created_at for %q: %w", id, err)
	}
	if e.ExpiresAt, err = time.Parse(time.RFC3339, expires); err != nil {
		return toolresult.Entry{}, fmt.Errorf("sqlstore: malformed expires_at for %q: %w", id, err)
	}
	e.Body, e.BodyStored = body.String, body.Valid
	return e, nil
}

// DeleteExpired removes entries whose expires_at text sorts before before.
func (s *Store) DeleteExpired(ctx context.Context, before time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, s.del, stamp(before))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
