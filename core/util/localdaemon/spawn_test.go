//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package localdaemon

import (
	"context"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestSpawnDetachesAndSurvivesParent: a helper process calls Spawn and exits;
// the daemon it started must still be running, in its own session, and must
// be stoppable. The test process never signals it: it ends via a stop file.
func TestSpawnDetachesAndSurvivesParent(t *testing.T) {
	path := tmpPath(t, "child")
	stopFile := path + ".child.stop"
	spawner := startHelperNoReady(t, "spawner", path)
	if sig := spawner.exitSignal(t); sig != 0 || !spawner.cmd.ProcessState.Success() {
		t.Fatalf("spawner failed: %v", spawner.cmd.ProcessState)
	}

	b, err := os.ReadFile(path) //nolint:gosec // G304: test-controlled path/binary
	if err != nil {
		t.Fatalf("spawner did not record the child pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(stopFile, nil, 0o600) })

	waitFor(t, "daemon to start", func() bool { _, err := os.Stat(path + ".child"); return err == nil })
	if !IsAlive(pid) {
		t.Fatal("spawned daemon died with its parent")
	}
	if pg, err := syscall.Getpgid(pid); err != nil || pg != pid {
		t.Fatalf("pgid = %d, %v; want its own group %d", pg, err, pid)
	}
	if pg, _ := syscall.Getpgid(os.Getpid()); pg == pid {
		t.Fatal("child shares this process's group")
	}

	if err := os.WriteFile(stopFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "daemon to exit after stop file", func() bool { return !IsAlive(pid) })
}

func TestSpawnProcessGroupDetach(t *testing.T) {
	path := tmpPath(t, "d")
	stopFile := path + ".stop"
	t.Cleanup(func() { _ = os.WriteFile(stopFile, nil, 0o600) })
	pid, err := Spawn(context.Background(), SpawnOptions{
		Detach: ProcessGroup,
		Env:    []string{helperEnv + "=daemon", helperPathEnv + "=" + path},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "daemon to start", func() bool { _, err := os.Stat(path); return err == nil })
	if pg, err := syscall.Getpgid(pid); err != nil || pg != pid {
		t.Fatalf("pgid = %d, %v; want %d", pg, err, pid)
	}
	if err := os.WriteFile(stopFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Spawn reaps in the background, so the PID must vanish, not turn zombie.
	waitFor(t, "spawned child to be reaped", func() bool { return !IsAlive(pid) })
}

func TestSpawnRedirectsOutputAndHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Spawn(ctx, SpawnOptions{}); err == nil {
		t.Fatal("Spawn with a canceled context should not start anything")
	}
	if _, err := Spawn(context.Background(), SpawnOptions{Detach: Detach(99)}); err == nil {
		t.Fatal("unknown Detach should be rejected")
	}
}

// TestSpawnContextCancelAfterStartDoesNotKillChild: ctx only bounds the start.
func TestSpawnContextCancelAfterStartDoesNotKillChild(t *testing.T) {
	path := tmpPath(t, "d")
	stopFile := path + ".stop"
	t.Cleanup(func() { _ = os.WriteFile(stopFile, nil, 0o600) })
	ctx, cancel := context.WithCancel(context.Background())
	pid, err := Spawn(ctx, SpawnOptions{Env: []string{helperEnv + "=daemon", helperPathEnv + "=" + path}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "daemon to start", func() bool { _, err := os.Stat(path); return err == nil })
	cancel()
	time.Sleep(200 * time.Millisecond)
	if !IsAlive(pid) {
		t.Fatal("canceling the Spawn context killed the daemon")
	}
}
