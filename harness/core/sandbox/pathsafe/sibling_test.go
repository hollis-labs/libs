package pathsafe

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A directory whose name merely starts with the root's name is a sibling, not a
// descendant. A symlink under the root that points at it must be refused: a raw
// string-prefix check ("/x/root" is a prefix of "/x/root-evil") would accept it.
// The seed's table has no case for this, so it lives here, separate from the
// ported tests.
func TestResolveUnder_SiblingWithRootAsPrefixIsRefused(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "root")
	sibling := filepath.Join(base, "root-evil")
	for _, d := range []string{root, sibling} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(sibling, filepath.Join(root, "link")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}

	for _, user := range []string{"link", "link/secret.txt", "link/new/deep.txt"} {
		got, err := ResolveUnder(root, user)
		var esc *EscapeError
		if !errors.As(err, &esc) {
			t.Errorf("ResolveUnder(%q) = %q, %v; want an *EscapeError", user, got, err)
		}
	}

	// Control: a symlink to a real subdirectory of the root is fine.
	inside := filepath.Join(root, "inside")
	if err := os.MkdirAll(inside, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, filepath.Join(root, "ok")); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveUnder(root, "ok/file.txt"); err != nil {
		t.Errorf("a symlink that stays inside the root was refused: %v", err)
	}
}
