package store_test

import (
	"testing"

	"github.com/hollis-labs/libs/core/store"
)

func TestRebind(t *testing.T) {
	cases := []struct{ in, want string }{
		{"SELECT 1", "SELECT 1"},
		{"SELECT * FROM t WHERE a = ? AND b = ?", "SELECT * FROM t WHERE a = $1 AND b = $2"},
		{"SELECT '?' , ?", "SELECT '?' , $1"},
		{"SELECT 'it''s ?', ?", "SELECT 'it''s ?', $1"},
		{`SELECT "col?" FROM t WHERE x = ?`, `SELECT "col?" FROM t WHERE x = $1`},
		{"SELECT ? -- is it?\n, ?", "SELECT $1 -- is it?\n, $2"},
		{"SELECT /* ? */ ?", "SELECT /* ? */ $1"},
		{"SELECT $$ ? $$, ?", "SELECT $$ ? $$, $1"},
		{"SELECT $tag$ ? $tag$, ?", "SELECT $tag$ ? $tag$, $1"},
		{"SELECT a ?? b, ?", "SELECT a ? b, $1"},
		{"SELECT '", "SELECT '"},
		{"/* unterminated ?", "/* unterminated ?"},
		{"SELECT $1, ?", "SELECT $1, $1"}, // pre-numbered placeholders are not mixed; documented
		{`SELECT E'it\'s ?', ?`, `SELECT E'it\'s ?', $1`},
		{`SELECT 'back\', ?`, `SELECT 'back\', $1`}, // plain string: backslash is not an escape
		{"SELECT a$b$c ?, ?", "SELECT a$b$c $1, $2"},
		{"SELECT /* a /* ? */ ? */ ?", "SELECT /* a /* ? */ ? */ $1"},
		{"SELECT x, ?? , ?", "SELECT x, ? , $1"},
	}
	for _, c := range cases {
		if got := store.Rebind(c.in); got != c.want {
			t.Errorf("Rebind(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func FuzzRebindNeverPanics(f *testing.F) {
	for _, s := range []string{"?", "'", "$$", "$a$", "/*", "--", `"`, "?? ?"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_ = store.Rebind(s)
		_ = store.Rebind(s + s)
	})
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestRebindSQLite(t *testing.T) {
	d, err := store.DialectFor(store.DriverSQLite)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ in, want string }{
		{"SELECT a ?? b, ?", "SELECT a ? b, ?"},
		{"SELECT '??' , ?? -- ??\n, ?", "SELECT '??' , ? -- ??\n, ?"},
		{"SELECT /* ?? */ ?", "SELECT /* ?? */ ?"},
		{"SELECT $$ ?? $$", "SELECT $$ ?? $$"},
	}
	for _, c := range cases {
		if got := d.Rebind(c.in); got != c.want {
			t.Errorf("sqlite Rebind(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
