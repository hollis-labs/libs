//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package localdaemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

func fakePS(t *testing.T, out string, err error) {
	t.Helper()
	old := runPS
	runPS = func(context.Context, int) ([]byte, error) { return []byte(out), err }
	t.Cleanup(func() { runPS = old })
}

func TestVerifyCommandWithFakeRunner(t *testing.T) {
	re := regexp.MustCompile(`(?:^|/)mydaemon\s+run(?:\s|$)`)
	ctx := context.Background()

	fakePS(t, "/usr/local/bin/mydaemon run --x\n", nil)
	if ok, err := VerifyCommand(ctx, 123, re); err != nil || !ok {
		t.Fatalf("match = %v, %v", ok, err)
	}
	fakePS(t, "/usr/bin/vim notes.txt\n", nil)
	if ok, err := VerifyCommand(ctx, 123, re); err != nil || ok {
		t.Fatalf("mismatch = %v, %v", ok, err)
	}
	fakePS(t, "", nil)
	if ok, err := VerifyCommand(ctx, 123, re); err != nil || ok {
		t.Fatalf("empty output = %v, %v", ok, err)
	}
	if ok, err := VerifyCommand(ctx, 0, re); err != nil || ok {
		t.Fatalf("pid 0 = %v, %v", ok, err)
	}
	if _, err := VerifyCommand(ctx, 123, nil); err == nil {
		t.Fatal("nil pattern must be an error")
	}
	boom := errors.New("ps: not found")
	fakePS(t, "", boom)
	if ok, err := VerifyCommand(ctx, 123, re); !errors.Is(err, boom) || ok {
		t.Fatalf("probe failure = %v, %v", ok, err)
	}
}

func TestVerifyCommandContextEndIsAnErrorNotAMismatch(t *testing.T) {
	old := runPS
	t.Cleanup(func() { runPS = old })
	ctx, cancel := context.WithCancel(context.Background())
	runPS = func(context.Context, int) ([]byte, error) {
		cancel()
		return nil, os.ErrClosed
	}
	if _, err := VerifyCommand(ctx, 123, regexp.MustCompile(".")); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestVerifyCommandRealPS runs the real ps against a helper process this test
// started, and against a PID that is gone.
func TestVerifyCommandRealPS(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	h := startHelper(t, "sleep", "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	self := regexp.MustCompile(regexp.QuoteMeta(filepath.Base(exe)))
	if ok, err := VerifyCommand(ctx, h.pid(), self); err != nil || !ok {
		t.Fatalf("own helper should match its binary name: %v, %v", ok, err)
	}
	other := regexp.MustCompile(`^/definitely/not/this/binary$`)
	if ok, err := VerifyCommand(ctx, h.pid(), other); err != nil || ok {
		t.Fatalf("mismatch should be (false, nil): %v, %v", ok, err)
	}

	pid := h.pid()
	_ = h.cmd.Process.Kill()
	h.exitSignal(t)
	if ok, err := VerifyCommand(ctx, pid, self); err != nil || ok {
		t.Fatalf("gone pid should be (false, nil): %v, %v", ok, err)
	}
}
