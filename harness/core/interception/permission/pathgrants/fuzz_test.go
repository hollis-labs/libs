package pathgrants

import (
	"strings"
	"testing"
)

func FuzzExtractPathMentions(f *testing.F) {
	for _, s := range []string{
		"", "/", "~", "~/", "./a", "/a/b.", "see ~/foo.", "`/x`", "**/tmp/y**",
		"https://example.com/x", "//share/x", "a\x00b /c", "  /a\t/b\n./c ",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, msg string) {
		got := ExtractPathMentions(msg)
		seen := map[string]bool{}
		for _, m := range got {
			if !isPathToken(m) {
				t.Fatalf("returned non-path token %q from %q", m, msg)
			}
			if strings.ContainsAny(m, " \t\n") {
				t.Fatalf("token %q contains whitespace", m)
			}
			if seen[m] {
				t.Fatalf("duplicate token %q", m)
			}
			seen[m] = true
		}
		// Registering the same message must not panic and only grants what
		// the mentions describe.
		g := NewPathGrants()
		g.RegisterFromUserMessage("s", msg)
	})
}
