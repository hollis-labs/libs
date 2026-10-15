//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package localdaemon

import (
	"context"
	"errors"
	"os"
	"regexp"
	"sync/atomic"
	"testing"
	"time"
)

func always(context.Context, int) (bool, error) { return true, nil }

func fastStop(v func(context.Context, int) (bool, error)) StopOptions {
	return StopOptions{
		GracePeriod:  400 * time.Millisecond,
		KillTimeout:  400 * time.Millisecond,
		PollInterval: 10 * time.Millisecond,
		Verify:       v,
	}
}

func TestStopTermExit(t *testing.T) {
	h := startHelper(t, "sleep", "")
	start := time.Now()
	if err := Stop(context.Background(), h.pid(), fastStop(always)); err != nil {
		t.Fatal(err)
	}
	if sig := h.exitSignal(t); sig != 15 {
		t.Fatalf("exit signal = %d, want 15 (SIGTERM, no escalation)", sig)
	}
	if time.Since(start) > 350*time.Millisecond {
		t.Fatalf("took %v: should not have waited out the grace period", time.Since(start))
	}
}

func TestStopEscalatesToKillWhenTermIgnored(t *testing.T) {
	h := startHelper(t, "ignore-term", "")
	start := time.Now()
	if err := Stop(context.Background(), h.pid(), fastStop(always)); err != nil {
		t.Fatal(err)
	}
	if sig := h.exitSignal(t); sig != 9 {
		t.Fatalf("exit signal = %d, want 9 (SIGKILL after grace)", sig)
	}
	if el := time.Since(start); el < 400*time.Millisecond {
		t.Fatalf("escalated after %v, before the 400ms grace period", el)
	}
}

// TestStopTimesOut uses a zombie: SIGKILL cannot remove it and kill(pid, 0)
// still succeeds, so Stop must give up with ErrStopTimeout instead of hanging.
func TestStopTimesOutWhenProcessWillNotDie(t *testing.T) {
	h := startZombie(t)
	start := time.Now()
	err := Stop(context.Background(), h, fastStop(always))
	if !errors.Is(err, ErrStopTimeout) {
		t.Fatalf("Stop = %v, want ErrStopTimeout", err)
	}
	if el := time.Since(start); el < 700*time.Millisecond || el > 3*time.Second {
		t.Fatalf("elapsed %v, want about grace+kill (800ms)", el)
	}
}

// TestStopRefusesOnIdentityMismatch is the regression test for the gap in
// Tether's stop path (CW-20260930-0045): a live PID that is not the daemon
// (PID reuse) must never be signaled.
func TestStopRefusesOnIdentityMismatch(t *testing.T) {
	h := startHelper(t, "sleep", "")
	notThisProgram := regexp.MustCompile(`^/definitely/not/this/binary$`)
	opts := fastStop(func(ctx context.Context, pid int) (bool, error) {
		return VerifyCommand(ctx, pid, notThisProgram)
	})
	err := Stop(context.Background(), h.pid(), opts)
	if !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("Stop = %v, want ErrIdentityMismatch", err)
	}
	time.Sleep(100 * time.Millisecond)
	if h.exited() || !IsAlive(h.pid()) {
		t.Fatal("Stop signaled a process whose identity did not verify")
	}
}

func TestStopVerifyErrorRefusesToSignal(t *testing.T) {
	h := startHelper(t, "sleep", "")
	boom := errors.New("probe failed")
	err := Stop(context.Background(), h.pid(), fastStop(func(context.Context, int) (bool, error) { return false, boom }))
	if !errors.Is(err, boom) {
		t.Fatalf("Stop = %v, want probe error", err)
	}
	time.Sleep(50 * time.Millisecond)
	if h.exited() {
		t.Fatal("Stop signaled despite a failed identity probe")
	}
}

func TestStopRequiresVerify(t *testing.T) {
	h := startHelper(t, "sleep", "")
	if err := Stop(context.Background(), h.pid(), StopOptions{}); !errors.Is(err, ErrVerifyRequired) {
		t.Fatalf("Stop = %v, want ErrVerifyRequired", err)
	}
	time.Sleep(50 * time.Millisecond)
	if h.exited() {
		t.Fatal("Stop signaled without a Verify hook")
	}
}

// TestStopReverifiesBeforeKill: the PID is checked again before SIGKILL, so a
// PID recycled during the grace period is not killed.
func TestStopReverifiesBeforeKill(t *testing.T) {
	h := startHelper(t, "ignore-term", "")
	var calls atomic.Int32
	verify := func(context.Context, int) (bool, error) {
		return calls.Add(1) == 1, nil // matches before TERM, mismatches after grace
	}
	if err := Stop(context.Background(), h.pid(), fastStop(verify)); err != nil {
		t.Fatalf("Stop = %v, want nil (original process deemed gone)", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("verify calls = %d, want 2", calls.Load())
	}
	time.Sleep(50 * time.Millisecond)
	if h.exited() {
		t.Fatal("Stop escalated to SIGKILL against a PID that no longer verified")
	}
}

func TestStopNeverSignalsInvalidPIDs(t *testing.T) {
	for _, pid := range []int{0, -1, -12345, os.Getpid()} {
		if err := Stop(context.Background(), pid, fastStop(always)); !errors.Is(err, ErrInvalidPID) {
			t.Fatalf("Stop(%d) = %v, want ErrInvalidPID", pid, err)
		}
	}
}

func TestStopAlreadyGoneIsNil(t *testing.T) {
	h := startHelper(t, "sleep", "")
	pid := h.pid()
	_ = h.cmd.Process.Kill()
	h.exitSignal(t)
	var called bool
	err := Stop(context.Background(), pid, fastStop(func(context.Context, int) (bool, error) { called = true; return true, nil }))
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("verify should not run for a dead PID")
	}
}

func TestStopContextCancelDoesNotEscalate(t *testing.T) {
	h := startHelper(t, "ignore-term", "")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	opts := fastStop(always)
	opts.GracePeriod = 5 * time.Second
	err := Stop(ctx, h.pid(), opts)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop = %v, want DeadlineExceeded", err)
	}
	if h.exited() {
		t.Fatal("canceled Stop must not SIGKILL")
	}
}
