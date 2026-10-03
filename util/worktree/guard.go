package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// goModReplaceRelRe matches a relative ("../" or "./") replace target. It is
// deliberately permissive: the guard only needs to know that some relative
// replace exists.
var goModReplaceRelRe = regexp.MustCompile(`=>\s+(\.\.?/[^\s]*)`)

// RelativeReplaceGuard refuses a placement that would break relative
// `replace` directives in the repository's go.mod. Relative targets resolve
// against the go.mod's own directory, so the worktree must sit at the same
// depth as the repository: worktree and repository must share a parent
// directory. A repository without a go.mod, or with no relative replaces,
// passes for any placement. It is Go-specific and off unless added with
// [WithGuard] (the [Torque] preset adds it).
func RelativeReplaceGuard() Guard {
	return func(_ context.Context, repoRoot, wtPath string) error {
		b, err := os.ReadFile(filepath.Join(repoRoot, "go.mod")) //nolint:gosec // path is the caller's repo root
		if err != nil || !goModReplaceRelRe.Match(b) {
			// No readable go.mod, or nothing depth-sensitive in it.
			return nil //nolint:nilerr // absence of a go.mod is not a failure
		}
		repoParent := filepath.Dir(canon(repoRoot))
		wtParent := filepath.Dir(canon(wtPath))
		if repoParent == wtParent {
			return nil
		}
		return fmt.Errorf(
			"worktree placement %s would break relative go.mod replace directives: "+
				"its go.mod sits at a different directory depth than the repo go.mod at %s "+
				"(worktree parent %s != repo parent %s); a relative \"replace ../...\" target would resolve to the wrong tree. "+
				"Use a sibling placement or a root at the same depth as the repo",
			wtPath, repoRoot, wtParent, repoParent)
	}
}
