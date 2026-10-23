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
		{"SELECT a FROM t WHERE doc ?| array['x']", "jsonb-operator"},
		{"SELECT a FROM t WHERE doc ?& array['x']", "jsonb-operator"},
		{"SELECT a FROM t LIMIT 10, 5", "limit-comma"},
		{"SELECT group_concat(a) FROM t", "sqlite-only"},
		{"CREATE TABLE t (id SERIAL PRIMARY KEY)", "autoincrement"},
		// Clean.
		{"SELECT a FROM t WHERE doc ??| array['x']", ""},
		{"SELECT serial FROM devices WHERE id = ?", ""},
		{"SELECT * FROM t WHERE lower(coalesce(a, b)) LIKE lower(?) ESCAPE '\\'", ""},
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
	_ = "SELECT a FROM t " +
		"WHERE b = datetime('now')"
	_ = "SELECT a " + col + " FROM t WHERE x ILIKE ?"
}
`
	fs, err := lint.File("x.go", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if got := rules(fs); got != "insert-or,last-insert-id,sqlite-time,ilike" {
		t.Fatalf("findings = %q: %v", got, fs)
	}
	if fs[0].Line != 6 || fs[1].Line != 7 {
		t.Errorf("lines = %d, %d", fs[0].Line, fs[1].Line)
	}
}

func TestFileFoldsConstantsAndAppends(t *testing.T) {
	src := `package x

import "database/sql"

const base = "SELECT a FROM t "
const where = "WHERE d = datetime('now')"
const joined = base + where

func f(db *sql.DB, col, pat string) {
	_ = base + "WHERE c ILIKE ?"
	q := "SELECT a FROM t"
	q += " WHERE x ILIKE ?"
	_ = q
	r := "SELECT a FROM t WHERE 1=1"
	r = r + " AND y = datetime('now')"
	_ = r
	_ = joined
	// Clean: the LIKE operand is built at run time, so nothing can be said.
	_ = "SELECT lower(a) " + col + " LIKE lower(?)"
	_ = "SELECT a FROM t WHERE " + col + " LIKE ?"
	_ = "SELECT a FROM t WHERE lower(a) LIKE lower(?) AND " + col + " = ?"
}
`
	fs, err := lint.File("x.go", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	got := map[int]string{}
	for _, f := range fs {
		got[f.Line] += f.Rule + ","
	}
	want := map[int]string{6: "sqlite-time,", 10: "ilike,", 12: "ilike,", 15: "sqlite-time,"}
	for line, rule := range want {
		if !strings.Contains(got[line], rule) {
			t.Errorf("line %d: want %s, got %q (all: %v)", line, rule, got[line], fs)
		}
	}
	for line, rule := range got {
		if line >= 18 {
			t.Errorf("false positive on line %d: %s", line, rule)
		}
	}
}

func TestFileRound3(t *testing.T) {
	src := `package x

import "database/sql"

// gostore:allow sqlite-time
const waivedBase = "SELECT a FROM t WHERE d = datetime('now')"

func a(db *sql.DB, col, pat string) {
	_ = "SELECT a FROM t WHERE name LIKE " + pat // line 9: flagged, the column side has no lower()
	q := "SELECT a FROM t WHERE lower(a)"
	q += " LIKE lower(?)" // clean: the pair is split across pieces
	_ = q
	_ = waivedBase + " AND b = ?" // clean: the waiver on the constant follows it
	_ = "INSERT INTO " + col + " DEFAULT VALUES" // line 14: sqlite-only
	_ = "SELECT a FROM t WHERE lower(a) LIKE " + pat // clean: the left side is lower()
	_ = "SELECT a FROM t WHERE x LIKE lower(" + pat + ")" // line 16: left side has no lower()
}

func b() {
	q := "hello"
	q += " world LIKE that" // clean: q is not SQL in this function
	_ = q
}
`
	fs, err := lint.File("x.go", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	got := map[int]string{}
	for _, f := range fs {
		got[f.Line] += f.Rule + ","
	}
	want := map[int]string{9: "like-case,", 14: "sqlite-only,", 16: "like-case,"}
	if len(got) != len(want) {
		t.Errorf("findings = %v, want lines %v", fs, want)
	}
	for line, rule := range want {
		if got[line] != rule {
			t.Errorf("line %d: got %q, want %q", line, got[line], rule)
		}
	}
}

func TestFileRound4(t *testing.T) {
	src := `package x

// gostore:allow sqlite-time
const waivedBase = "SELECT a FROM t WHERE d = datetime('now')"

func a(pat string) {
	_ = waivedBase + " AND e = datetime('now')" // line 7: a new violation beside a waived constant
	q := "SELECT a FROM t WHERE d = datetime('now')" // gostore:allow sqlite-time
	_ = q
	q += " AND e = strftime('%s', 'now')" // line 10: new, the waiver was for the first
	r := "SELECT a FROM t WHERE x ILIKE ?" // gostore:allow ilike
	_ = r
	r += " AND y ILIKE ?" // line 13
	_ = "WHERE name LIKE " + pat // line 14: a clause fragment is linted
	_ = "INSERT INTO \"t\" DEFAULT VALUES" // line 15
	_ = "INSERT INTO s.t DEFAULT VALUES" // line 16
	_ = waivedBase + " AND f = ?" // clean
}
`
	fs, err := lint.File("x.go", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	got := map[int]string{}
	for _, f := range fs {
		got[f.Line] += f.Rule + ","
	}
	want := map[int]string{7: "sqlite-time,", 10: "sqlite-time,", 13: "ilike,", 14: "like-case,", 15: "sqlite-only,", 16: "sqlite-only,"}
	if len(got) != len(want) {
		t.Errorf("findings = %v", fs)
	}
	for line, rule := range want {
		if got[line] != rule {
			t.Errorf("line %d: got %q, want %q", line, got[line], rule)
		}
	}
}
