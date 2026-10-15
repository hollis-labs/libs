//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package localdaemon

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// The tests re-exec the test binary as helper child processes, selected by
// LOCALDAEMON_HELPER. Every helper exits by itself after helperMaxLife so a
// crashed test cannot leave a process behind, and tests only ever signal PIDs
// of children they started themselves.
const (
	helperEnv     = "LOCALDAEMON_HELPER"
	helperPathEnv = "LOCALDAEMON_HELPER_PATH"
	helperMaxLife = 60 * time.Second
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		runHelper(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runHelper implements the child-process modes. Each prints "ready" once its
// setup is done.
func runHelper(mode string) {
	path := os.Getenv(helperPathEnv)
	deadline := time.After(helperMaxLife)
	switch mode {
	case "hold-lock":
		l, err := TryAcquire(path)
		if err != nil {
			os.Stdout.WriteString("error: " + err.Error() + "\n")
			return
		}
		defer l.Release()
		os.Stdout.WriteString("ready\n")
		<-deadline
	case "exit": // exits at once; used to make a zombie
		return
	case "sleep": // default SIGTERM disposition: dies on TERM
		os.Stdout.WriteString("ready\n")
		<-deadline
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
		os.Stdout.WriteString("ready\n")
		<-deadline
	case "daemon": // Spawn target: runs until the stop file appears
		stop := path + ".stop"
		os.WriteFile(path, []byte("up\n"), 0o600) //nolint:gosec // G703: test-controlled path/binary
		for {
			if _, err := os.Stat(stop); err == nil { //nolint:gosec // G703: test-controlled path/binary
				return
			}
			select {
			case <-deadline:
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	case "spawner": // calls Spawn, records the child's PID, and exits
		pid, err := Spawn(context.Background(), SpawnOptions{Env: []string{helperEnv + "=daemon", helperPathEnv + "=" + path + ".child"}})
		if err != nil {
			os.Stdout.WriteString("error: " + err.Error() + "\n")
			return
		}
		os.WriteFile(path, []byte(strconv.Itoa(pid)), 0o600) //nolint:gosec // G703: test-controlled path/binary
	}
}

// startHelper runs a helper child, waits for its "ready" line, and registers
// cleanup that kills only that child.
func startHelper(t *testing.T, mode, path string) *helperProc {
	t.Helper()
	return startHelperWith(t, mode, path, true)
}

// startHelperNoReady starts a helper that prints nothing and just exits.
func startHelperNoReady(t *testing.T, mode, path string) *helperProc {
	t.Helper()
	return startHelperWith(t, mode, path, false)
}

func startHelperWith(t *testing.T, mode, path string, wantReady bool) *helperProc {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe) //nolint:gosec // G204: test-controlled path/binary
	cmd.Env = append(os.Environ(), helperEnv+"="+mode, helperPathEnv+"="+path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h := &helperProc{cmd: cmd, done: make(chan struct{})}
	go func() {
		h.waitErr = cmd.Wait()
		close(h.done)
	}()
	t.Cleanup(func() {
		select {
		case <-h.done:
		default:
			_ = cmd.Process.Kill()
			<-h.done
		}
	})
	if !wantReady {
		return h
	}
	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(out).ReadString('\n')
		line <- s
	}()
	select {
	case s := <-line:
		if s != "ready\n" {
			t.Fatalf("helper %s did not become ready: %q", mode, s)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("helper %s not ready in time", mode)
	}
	return h
}

// helperProc is a child the test started itself and reaps itself.
type helperProc struct {
	cmd     *exec.Cmd
	done    chan struct{}
	waitErr error
}

func (h *helperProc) pid() int { return h.cmd.Process.Pid }

// exitSignal waits for the child to exit and returns the signal that killed
// it (0 if it exited normally).
func (h *helperProc) exitSignal(t *testing.T) syscall.Signal {
	t.Helper()
	select {
	case <-h.done:
	case <-time.After(10 * time.Second):
		t.Fatal("helper did not exit")
	}
	if ws, ok := h.cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return ws.Signal()
	}
	return 0
}

func (h *helperProc) exited() bool {
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

// startZombie starts a child that exits immediately and deliberately does not
// reap it until cleanup, leaving a zombie: signal 0 succeeds on it and
// SIGKILL cannot remove it.
func startZombie(t *testing.T) int {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe) //nolint:gosec // G204: test-controlled path/binary
	cmd.Env = append(os.Environ(), helperEnv+"=exit")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	time.Sleep(300 * time.Millisecond)
	return cmd.Process.Pid
}
