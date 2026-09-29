package sqlstore_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	toolresult "github.com/hollis-labs/go-toolresult"
	"github.com/hollis-labs/go-toolresult/sqlstore"
	"github.com/hollis-labs/go-toolresult/storetest"
)

// nanite is migration 011 of Nanite, verbatim apart from comments.
const nanite = `CREATE TABLE IF NOT EXISTS tool_result_cache (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    tool_call_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    byte_size INTEGER NOT NULL,
    was_truncated INTEGER NOT NULL DEFAULT 0,
    body TEXT
);
CREATE INDEX IF NOT EXISTS idx_tool_result_cache_session ON tool_result_cache(session_id);
CREATE INDEX IF NOT EXISTS idx_tool_result_cache_expires ON tool_result_cache(expires_at);`

// loom is migration 009 of Loom, verbatim apart from comments. Note the
// created_at default, the caller_id column and the extra index.
const loom = `CREATE TABLE IF NOT EXISTS wiki_result_cache (
    id TEXT PRIMARY KEY,
    caller_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    tool_call_id TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    expires_at TEXT NOT NULL,
    byte_size INTEGER NOT NULL,
    was_truncated INTEGER NOT NULL DEFAULT 0,
    body TEXT
);
CREATE INDEX IF NOT EXISTS idx_wiki_result_cache_caller ON wiki_result_cache (caller_id, tool_name);
CREATE INDEX IF NOT EXISTS idx_wiki_result_cache_expires ON wiki_result_cache (expires_at);`

func openDB(t *testing.T, ddl string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // every :memory: connection is its own database
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestConformanceNaniteTable(t *testing.T) {
	storetest.Run(t, func(t *testing.T) toolresult.Store { return sqlstore.New(openDB(t, nanite), sqlstore.Table{}) })
}

func TestConformanceLoomTable(t *testing.T) {
	storetest.Run(t, func(t *testing.T) toolresult.Store {
		return sqlstore.New(openDB(t, loom), sqlstore.Table{Name: "wiki_result_cache", ScopeColumn: "caller_id"})
	})
}

func TestConformanceGeneratedDDL(t *testing.T) {
	tbl := sqlstore.Table{Name: "results", ScopeColumn: "owner"}
	storetest.Run(t, func(t *testing.T) toolresult.Store { return sqlstore.New(openDB(t, sqlstore.DDL(tbl)), tbl) })
}

func TestDefaultDDLMatchesNaniteColumns(t *testing.T) {
	db := openDB(t, sqlstore.DDL(sqlstore.Table{}))
	rows, err := db.Query(`SELECT name FROM pragma_table_info('tool_result_cache') ORDER BY cid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	want := "id session_id tool_name tool_call_id created_at expires_at byte_size was_truncated body"
	if got := strings.Join(cols, " "); got != want {
		t.Fatalf("columns = %q; want %q", got, want)
	}
}

// TestReadsRowsWrittenByTheApps proves a row written by Nanite's or Loom's
// own SQL (not by this package) is readable, including Loom's fractional
// created_at default and Nanite's NULL body for an over-cap result.
func TestReadsRowsWrittenByTheApps(t *testing.T) {
	ctx := context.Background()
	db := openDB(t, nanite)
	if _, err := db.Exec(`INSERT INTO tool_result_cache (id, session_id, tool_name, tool_call_id, created_at, expires_at, byte_size, was_truncated, body)
 VALUES ('n1', 'sess', 'tool', 'call', '2026-01-01T00:00:00Z', '2026-01-01T01:00:00Z', 4, 1, 'data'),
        ('n2', 'sess', 'tool', 'call', '2026-01-01T00:00:00Z', '2026-01-01T01:00:00Z', 9999, 1, NULL)`); err != nil {
		t.Fatal(err)
	}
	s := sqlstore.New(db, sqlstore.Table{})
	if e, err := s.Get(ctx, "sess", "n1"); err != nil || e.Body != "data" || !e.BodyStored {
		t.Fatalf("n1: %+v, %v", e, err)
	}
	if e, err := s.Get(ctx, "sess", "n2"); err != nil || e.BodyStored || e.ByteSize != 9999 {
		t.Fatalf("n2: %+v, %v", e, err)
	}

	ldb := openDB(t, loom)
	if _, err := ldb.Exec(`INSERT INTO wiki_result_cache (id, caller_id, tool_name, tool_call_id, expires_at, byte_size, body) VALUES ('l1', 'me', 'wiki', 'c', '2999-01-01T00:00:00Z', 2, 'ok')`); err != nil {
		t.Fatal(err)
	}
	ls := sqlstore.New(ldb, sqlstore.Table{Name: "wiki_result_cache", ScopeColumn: "caller_id"})
	e, err := ls.Get(ctx, "me", "l1")
	if err != nil || e.Body != "ok" || e.CreatedAt.IsZero() || e.CreatedAt.After(time.Now().Add(time.Minute)) {
		t.Fatalf("l1: %+v, %v", e, err)
	}
	cache := toolresult.New(ls, toolresult.Config{})
	if page, err := cache.Read(ctx, "me", "l1", "", 0, 0, 0); err != nil || page.Content != "ok" {
		t.Fatalf("cache over a Loom-written row: %+v, %v", page, err)
	}
}

func TestInvalidIdentifiersPanic(t *testing.T) {
	for _, tbl := range []sqlstore.Table{
		{Name: "a; DROP TABLE x"},
		{Name: "ok", ScopeColumn: "s c"},
		{Name: "1abc"},
		{Name: "a-b"},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("New(%+v) did not panic", tbl)
				}
			}()
			sqlstore.New(nil, tbl)
		}()
	}
}
