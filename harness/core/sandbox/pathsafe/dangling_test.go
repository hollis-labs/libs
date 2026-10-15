package pathsafe

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A symlink as the FINAL path component whose target does not exist yet is a
// dangling link. A caller that then creates the returned path writes through
// the link to wherever it points, so ResolveUnder must follow it and judge the
// destination. The seed only looked at the parent of a non-existent path and so
// returned root/link for a link pointing outside the root.

func danglingFixture(t *testing.T) (root, outside string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(base, "root")
	outside = filepath.Join(base, "outside")
	for _, d := range []string{root, outside, filepath.Join(root, "sub")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	return root, outside
}

func link(t *testing.T, target, at string) {
	t.Helper()
	if err := os.Symlink(target, at); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
}

func wantEscape(t *testing.T, root, user string) {
	t.Helper()
	got, err := ResolveUnder(root, user)
	var esc *EscapeError
	if !errors.As(err, &esc) {
		t.Errorf("ResolveUnder(%q) = %q, %v; want an *EscapeError", user, got, err)
	}
}

func TestResolveUnder_DanglingFinalLinkToOutsideIsRefused(t *testing.T) {
	root, outside := danglingFixture(t)
	link(t, filepath.Join(outside, "newfile"), filepath.Join(root, "link"))
	wantEscape(t, root, "link")
}

func TestResolveUnder_DanglingFinalLinkWhoseTargetParentIsMissingIsRefused(t *testing.T) {
	root, outside := danglingFixture(t)
	link(t, filepath.Join(outside, "no", "such", "dir", "f"), filepath.Join(root, "link"))
	wantEscape(t, root, "link")
}

func TestResolveUnder_ChainOfDanglingLinksEndingOutsideIsRefused(t *testing.T) {
	root, outside := danglingFixture(t)
	link(t, filepath.Join(outside, "newfile"), filepath.Join(root, "b"))
	link(t, filepath.Join(root, "b"), filepath.Join(root, "a"))
	wantEscape(t, root, "a")
}

func TestResolveUnder_DanglingRelativeLinkToOutsideIsRefused(t *testing.T) {
	root, _ := danglingFixture(t)
	link(t, filepath.Join("..", "outside", "x"), filepath.Join(root, "link"))
	wantEscape(t, root, "link")
	// Relative to the link's own directory, not the root's.
	link(t, filepath.Join("..", "..", "outside", "x"), filepath.Join(root, "sub", "deep"))
	wantEscape(t, root, "sub/deep")
}

func TestResolveUnder_DanglingLinkWithInsideTargetIsAllowed(t *testing.T) {
	root, _ := danglingFixture(t)
	link(t, filepath.Join(root, "sub", "later.txt"), filepath.Join(root, "abs"))
	link(t, filepath.Join("sub", "later2.txt"), filepath.Join(root, "rel"))
	link(t, filepath.Join("..", "sub", "later3.txt"), filepath.Join(root, "sub", "up"))
	for user, want := range map[string]string{
		"abs":    filepath.Join(root, "sub", "later.txt"),
		"rel":    filepath.Join(root, "sub", "later2.txt"),
		"sub/up": filepath.Join(root, "sub", "later3.txt"),
	} {
		got, err := ResolveUnder(root, user)
		if err != nil {
			t.Errorf("ResolveUnder(%q): %v; a dangling link that stays inside the root must be allowed", user, err)
			continue
		}
		if got != want {
			t.Errorf("ResolveUnder(%q) = %q, want %q", user, got, want)
		}
	}
}

// A cycle of links never resolves; it must fail, not loop.
func TestResolveUnder_LinkCycleFails(t *testing.T) {
	root, _ := danglingFixture(t)
	link(t, filepath.Join(root, "b"), filepath.Join(root, "a"))
	link(t, filepath.Join(root, "a"), filepath.Join(root, "b"))
	if got, err := ResolveUnder(root, "a"); err == nil {
		t.Errorf("ResolveUnder over a link cycle = %q, nil; want an error", got)
	}
}
