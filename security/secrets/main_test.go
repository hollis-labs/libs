package secrets_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain canonicalizes TMPDIR so t.TempDir paths survive the production
// symlink checks on macOS, where /var is a symlink to /private/var.
func TestMain(m *testing.M) {
	resolved, err := filepath.EvalSymlinks(os.TempDir())
	if err == nil {
		err = os.Setenv("TMPDIR", resolved)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "canonical temp dir:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
