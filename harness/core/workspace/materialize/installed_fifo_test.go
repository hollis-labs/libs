//go:build linux || darwin

package materialize

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A synchronous bounded inspect must reject the static FIFO before opening it.
// O_NONBLOCK on the defensive open also keeps a replaced type from hanging.
func TestInstalledStaticFIFORefusedBeforeMutation(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, ".claude/fixture.txt"), 0600); err != nil {
		t.Fatal(err)
	}
	req := installedFixtureRequest(t, root)
	h, err := NewEngine(EngineOptions{}).Apply(context.Background(), req)
	if !errors.Is(err, ErrUnsafeTarget) || h.Mutated {
		t.Fatal("FIFO accepted or staged")
	}
}

func TestInstalledDefensiveOpenDoesNotFollowEvenConfinedLink(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ordinary"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("ordinary", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := openInstalledRegular(root, "link")
	if file != nil {
		file.Close()
	}
	if err == nil {
		t.Fatal("defensive open followed link")
	}
}

func TestInstalledDefensiveOpenRejectsIntermediateLink(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "ordinary"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(dir, "parent")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := openInstalledRegular(root, "parent/ordinary")
	if file != nil {
		file.Close()
	}
	if err == nil {
		t.Fatal("followed intermediate link outside pinned root")
	}
}
