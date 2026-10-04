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
