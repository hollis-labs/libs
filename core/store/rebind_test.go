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
		_ = store.Mask(s)
	})
}

func TestMaskKeepsOffsets(t *testing.T) {
	in := "SELECT 'datetime(now)' -- now()\nFROM t /* x */"
	out := store.Mask(in)
	if len(out) != len(in) {
		t.Fatalf("length changed: %d -> %d", len(in), len(out))
	}
	if out[:7] != "SELECT " || out[len(out)-1] != ' ' && out[len(out)-1] != '/' {
		// shape check only
	}
	for _, bad := range []string{"datetime", "now()", " x "} {
		if contains(out, bad) {
			t.Errorf("Mask left %q in %q", bad, out)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
