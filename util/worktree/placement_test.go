package worktree_test

import (
	"path/filepath"
	"testing"

	worktree "github.com/hollis-labs/libs/util/worktree"
)

func TestSiblingPlacement(t *testing.T) {
	p := worktree.Sibling("run-")
	got := p.Path("/home/dev/myrepo", "42")
	if want := "/home/dev/myrepo-worktrees-run-42"; got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
	if filepath.Dir(got) != "/home/dev" {
		t.Fatalf("worktree parent %q must equal repo parent /home/dev", filepath.Dir(got))
	}
	if id, ok := p.ID("/home/dev/myrepo", got); !ok || id != "42" {
		t.Fatalf("ID = %q, %v; want 42, true", id, ok)
	}
	for _, foreign := range []string{
		"/home/dev/myrepo",
		"/home/dev/other-worktrees-run-42",
		"/home/dev/myrepo-worktrees-42",
		"/elsewhere/myrepo-worktrees-run-42",
		"/home/dev/myrepo-worktrees-run-",
	} {
		if id, ok := p.ID("/home/dev/myrepo", foreign); ok {
			t.Errorf("ID(%q) = %q, true; want not ours", foreign, id)
		}
	}
	if got := worktree.Sibling("").Path("/r/x", "a"); got != "/r/x-worktrees-a" {
		t.Errorf("default sibling = %q", got)
	}
}

func TestUnderRootPlacement(t *testing.T) {
	p := worktree.UnderRoot("/var/wt", "run-")
	got := p.Path("/repo", "9")
	if got != "/var/wt/run-9" {
		t.Fatalf("Path = %q", got)
	}
	if id, ok := p.ID("/repo", got); !ok || id != "9" {
		t.Fatalf("ID = %q, %v", id, ok)
	}
	if _, ok := p.ID("/repo", "/var/wt/deeper/run-9"); ok {
		t.Error("nested path must not match")
	}
	if _, ok := p.ID("/repo", "/var/wt/9"); ok {
		t.Error("missing prefix must not match")
	}
	rel := worktree.UnderRoot(".nanite/worktrees", "")
	if got := rel.Path("/repo", "s1"); got != "/repo/.nanite/worktrees/s1" {
		t.Errorf("relative root resolved to %q", got)
	}
}

func TestNestedPlacement(t *testing.T) {
	p := worktree.Nested("/ws", "repo")
	got := p.Path("/r", "session-123")
	if got != "/ws/session-123/repo" {
		t.Fatalf("Path = %q", got)
	}
	if id, ok := p.ID("/r", got); !ok || id != "session-123" {
		t.Fatalf("ID = %q, %v", id, ok)
	}
	for _, foreign := range []string{"/ws/session-123", "/ws/session-123/other", "/ws/a/b/repo", "/x/session-123/repo"} {
		if id, ok := p.ID("/r", foreign); ok {
			t.Errorf("ID(%q) = %q, true; want not ours", foreign, id)
		}
	}
}

// TestPlacementRoundTripThroughSymlinks: git reports resolved paths, so ID must
// accept the resolved spelling of a path the placement produced unresolved.
func TestPlacementRoundTripThroughSymlinks(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "real")
	link := filepath.Join(tmp, "link")
	if err := mkdirAndSymlink(target, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	p := worktree.UnderRoot(link, "w-")
	unresolved := p.Path("/repo", "1")
	if id, ok := p.ID("/repo", filepath.Join(target, "w-1")); !ok || id != "1" {
		t.Fatalf("resolved spelling of %q not recognized: %q %v", unresolved, id, ok)
	}
}
