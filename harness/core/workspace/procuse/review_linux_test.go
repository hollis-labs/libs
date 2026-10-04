//go:build linux

package procuse

import (
	"context"
	"os"
	"syscall"
	"testing"
)

func TestReviewFilesystemVisibility(t *testing.T) {
	f, options := fixture(t)
	for _, target := range []string{"/proc", "/sys", "/dev/shm"} {
		if _, err := os.Stat(target); err != nil {
			continue
		}
		var a, b syscall.Stat_t
		if err := syscall.Stat(target, &a); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Stat(options.ScratchDir, &b); err != nil {
			t.Fatal(err)
		}
		if target == "/dev/shm" && a.Dev == b.Dev {
			continue
		}
		f.target = target
		if got := Check(context.Background(), target, f.run, options); got.Outcome != Unknown {
			t.Errorf("unproven filesystem accepted: %+v", got)
		}
	}
}
