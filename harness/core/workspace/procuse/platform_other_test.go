//go:build !unix

package procuse

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestUnsupportedPlatformRetains(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	result := Check(context.Background(), target, ExecRunner(""), Options{ScratchDir: root})
	if result.Outcome != Unknown || result.Code != ReasonUnsupportedPlatform || result.SafeToClean() {
		t.Fatalf("unsupported platform: %+v", result)
	}
}
