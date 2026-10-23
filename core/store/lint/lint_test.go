package lint_test

import (
	"strings"
	"testing"

	"github.com/hollis-labs/libs/core/store/lint"
)

func rules(fs []lint.Finding) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Rule)
	}
	return strings.Join(out, ",")
}

func TestCheck(t *testing.T) {
	cases := []struct{ sql, want string }{
		{"INSERT OR IGNORE INTO t (a) VALUES (?)", "insert-or"},
		{"insert or replace into t(a) values (?)", "insert-or"},
		{"REPLACE INTO t (a) VALUES (?)", "insert-or"},
		{"SELECT datetime('now')", "sqlite-time"},
		{"UPDATE t SET at = now() WHERE id = ?", "db-clock"},
		{"CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT)", "autoincrement"},
		{"CREATE TABLE t (id BIGSERIAL)", "autoincrement"},
		{"SELECT * FROM t WHERE a ILIKE ?", "ilike"},
		{"SELECT json_extract(doc, '$.a') FROM t", "json-path"},
		{"SELECT doc #>> '{a}' FROM t", "json-path"},
		{"SELECT * FROM t WHERE a = $1", "dollar-placeholder"},
		{"PRAGMA foreign_keys = ON", "sqlite-only"},
		{"SELECT * FROM t WHERE name LIKE ?", "like-case"},
		// Clean.
		{"SELECT * FROM t WHERE lower(name) LIKE lower(?) ESCAPE '\\'", ""},
		{"INSERT INTO t (a) VALUES (?) ON CONFLICT (a) DO NOTHING", ""},
		{"SELECT 'datetime(now) INSERT OR IGNORE' FROM t -- now()\n WHERE a = ?", ""},
		{"SELECT \"now()\" FROM t", ""},
	}
	for _, c := range cases {
		if got := rules(lint.Check(c.sql)); got != c.want {
			t.Errorf("Check(%q) = %q, want %q", c.sql, got, c.want)
		}
	}
}

func TestFile(t *testing.T) {
	src := `package x

import "database/sql"

func f(db *sql.DB) {
	res, _ := db.Exec("INSERT OR IGNORE INTO t (a) VALUES (?)", 1)
	_, _ = res.LastInsertId()
	_, _ = db.Exec("INSERT OR IGNORE INTO t (a) VALUES (?)", 1) // gostore:allow insert-or
	// gostore:allow
	_, _ = db.Exec("PRAGMA user_version")
	_ = "not sql: select the best"
	_ = "SELECT a FROM t WHERE b = ?"
}
`
	fs, err := lint.File("x.go", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if got := rules(fs); got != "insert-or,last-insert-id" {
		t.Fatalf("findings = %q: %v", got, fs)
	}
	if fs[0].Line != 6 || fs[1].Line != 7 {
		t.Errorf("lines = %d, %d", fs[0].Line, fs[1].Line)
	}
}
