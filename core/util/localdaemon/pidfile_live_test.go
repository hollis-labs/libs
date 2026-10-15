//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package localdaemon

import (
	"errors"
	"testing"
)

// TestPIDFileStaleDetection: a PID file left by a process that has since
// exited is stale (IsAlive false) and may be overwritten.
func TestPIDFileStaleDetection(t *testing.T) {
	f := PIDFile{Path: tmpPath(t, "d.pid")}
	h := startHelper(t, "sleep", "")
	dead := h.pid()
	if err := f.Write(dead); err != nil {
		t.Fatal(err)
	}
	if pid, err := f.Read(); err != nil || !IsAlive(pid) {
		t.Fatalf("live daemon: pid=%d err=%v alive=%v", pid, err, IsAlive(pid))
	}
	_ = h.cmd.Process.Kill()
	h.exitSignal(t)

	pid, err := f.Read()
	if err != nil {
		t.Fatal(err)
	}
	if IsAlive(pid) {
		t.Fatalf("pid %d should be stale", pid)
	}
	if err := f.Write(4242); err != nil {
		t.Fatalf("stale pid file should be overwritable: %v", err)
	}
}

func TestPIDFileWriteRefusesLiveOtherPID(t *testing.T) {
	f := PIDFile{Path: tmpPath(t, "d.pid")}
	h := startHelper(t, "sleep", "")
	if err := f.Write(h.pid()); err != nil {
		t.Fatal(err)
	}
	if err := f.Write(h.pid() + 1); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Write over live pid = %v, want ErrAlreadyRunning", err)
	}
	if pid, _ := f.Read(); pid != h.pid() {
		t.Fatalf("refused Write changed the file: %d", pid)
	}
}

func TestIsAlive(t *testing.T) {
	for _, pid := range []int{0, -1} {
		if IsAlive(pid) {
			t.Fatalf("IsAlive(%d) = true", pid)
		}
	}
	h := startHelper(t, "sleep", "")
	if !IsAlive(h.pid()) {
		t.Fatal("helper should be alive")
	}
}
