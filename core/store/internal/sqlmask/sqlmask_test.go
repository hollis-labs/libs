package sqlmask_test

import (
	"strings"
	"testing"

	"github.com/hollis-labs/libs/core/store/internal/sqlmask"
)

func TestMask(t *testing.T) {
	cases := []struct {
		in    string
		gone  []string
		stays []string
	}{
		{"SELECT 'datetime(now)' -- now()\nFROM t /* x */", []string{"datetime", "now()", " x "}, []string{"SELECT", "FROM t"}},
		{"SELECT $$ INSERT OR IGNORE $$ FROM t", []string{"INSERT OR"}, []string{"FROM t"}},
		{"SELECT $q$ now() $q$, a$b$c FROM t", []string{"now()"}, []string{"a$b$c"}},
		{`SELECT E'it\'s now()' FROM t`, []string{"now()"}, []string{"FROM t"}},
		{"SELECT /* a /* now() */ now() */ b FROM t", []string{"now()"}, []string{"b FROM t"}},
	}
	for _, c := range cases {
		out := sqlmask.Mask(c.in)
		if len(out) != len(c.in) {
			t.Fatalf("%q: length changed %d -> %d", c.in, len(c.in), len(out))
		}
		for _, g := range c.gone {
			if strings.Contains(out, g) {
				t.Errorf("Mask(%q) = %q still holds %q", c.in, out, g)
			}
		}
		for _, k := range c.stays {
			if !strings.Contains(out, k) {
				t.Errorf("Mask(%q) = %q lost %q", c.in, out, k)
			}
		}
	}
}

func FuzzScanCovers(f *testing.F) {
	for _, s := range []string{"?", "'", "$$", "$a$", "/*", "--", `"`, "E'\\"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		at := 0
		for _, sp := range sqlmask.Scan(s) {
			if sp.From != at || sp.To <= sp.From {
				t.Fatalf("spans not contiguous at %d: %+v", at, sp)
			}
			at = sp.To
		}
		if at != len(s) {
			t.Fatalf("spans end at %d of %d", at, len(s))
		}
	})
}
