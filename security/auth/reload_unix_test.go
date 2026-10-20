//go:build unix

package auth

import (
	"context"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
)

func TestReloaderSIGHUP(t *testing.T) {
	var n atomic.Int32
	startReloader(t, &Reloader{Load: func(context.Context) error { n.Add(1); return nil }})
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "SIGHUP reload", func() bool { return n.Load() == 1 })
}
