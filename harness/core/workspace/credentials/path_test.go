package credentials_test

import (
	"github.com/hollis-labs/substrate/harness/workspace/credentials"
	"github.com/hollis-labs/substrate/harness/workspace/render"
	"strings"
	"testing"
)

// This test pins the shared slash-relative policy while keeping render out of
// the credential production import graph. Destinations further require ASCII.
func TestResourcePathPolicy(t *testing.T) {
	cases := []struct {
		path     string
		accepted bool
	}{
		{"a", true}, {"nested/credentials.json", true}, {".config/provider/auth", true}, {"résumé", true}, {"a b", true}, {strings.Repeat("a", 255), true}, {strings.TrimSuffix(strings.Repeat("a/", 64), "/"), true},
		{"", false}, {" ", false}, {"/a", false}, {"./a", false}, {"a/", false}, {"a//b", false}, {"a/./b", false}, {"a/../b", false}, {"../a", false}, {".", false}, {"..", false}, {"a\\b", false}, {"a\x00b", false}, {".GIT/x", false}, {".ssh", false}, {".gnupg/a", false}, {".aws/a", false}, {".materialize/x", false}, {"a:b", false}, {"{name}", false}, {"a\nb", false}, {"a\tb", false}, {"a\u202eb", false}, {"a\u2069b", false}, {strings.Repeat("a", 256), false}, {strings.Repeat("a/", 64) + "a", false}, {string([]byte{255}), false},
	}
	for _, tc := range cases {
		if got := credentials.ValidateRelPath(tc.path) == nil; got != tc.accepted {
			t.Errorf("credential path %q accepted=%v", tc.path, got)
		}
		if got := render.ValidateRelPath(tc.path) == nil; got != tc.accepted {
			t.Errorf("render path %q accepted=%v", tc.path, got)
		}
	}
	for _, p := range []string{"café", "cafe\u0301", "K", "\ufb01"} {
		if credentials.ValidateDestination(p) == nil {
			t.Errorf("Unicode destination accepted %q", p)
		}
	}
}
