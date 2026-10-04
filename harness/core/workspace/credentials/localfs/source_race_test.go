//go:build linux || darwin

package localfs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The worker is killed on a blocked open, so the failing case leaves no hung
// goroutine holding a root descriptor or mutation lock in the parent suite.
func TestSourceFifoSwapNeverBlocks(t *testing.T) {
	if os.Getenv("CREDENTIAL_FIFO_WITNESS") == "1" {
		g, _, p, _ := realFixture(t)
		p.openSource = func(root *os.Root, rel string) (*os.File, error) {
			path := filepath.Join(g.Home.LogicalPath, rel)
			if e := os.Remove(path); e != nil {
				t.Fatal(e)
			}
			if e := syscall.Mkfifo(path, 0600); e != nil {
				t.Fatal(e)
			}
			return openSourceReadOnly(root, rel)
		}
		if _, e := p.Source(context.Background(), g.Home, "a"); e == nil {
			t.Fatal("accepted swapped FIFO")
		}
		return
	}
	executable, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestSourceFifoSwapNeverBlocks$")
	cmd.Env = append(os.Environ(), "CREDENTIAL_FIFO_WITNESS=1")
	output, e := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal("Source blocked after regular-file/FIFO swap")
	}
	if e != nil {
		t.Fatalf("worker: %v %s", e, output)
	}
}
