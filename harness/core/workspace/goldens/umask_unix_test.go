//go:build unix

package goldens

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSandboxPinsAndRestoresUmask(t *testing.T) {
	old := syscall.Umask(0077)
	t.Cleanup(func() { syscall.Umask(old) })
	t.Run("fixture", func(t *testing.T) {
		root := Sandbox(t)
		path := filepath.Join(root, "ordinary.txt")
		if err := os.WriteFile(path, []byte("fixture"), 0644); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0644 {
			t.Errorf("fixture mode = %04o; want 0644", info.Mode().Perm())
		}
	})
	if got := syscall.Umask(0077); got != 0077 {
		t.Errorf("umask was not restored: %03o", got)
	}
}
