//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package localdaemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// Stop terminates the process pid: verify identity, SIGTERM, wait up to
// GracePeriod, re-verify, SIGKILL, wait up to KillTimeout.
//
// It returns nil when the process is gone (including when it was already
// gone, or when it exited and its PID was taken by a different program
// during the grace period). It refuses pid <= 0 and the calling process with
// [ErrInvalidPID], and never signals when identity is unverified. It returns
// ctx.Err() (wrapped) if ctx ends first, without escalating.
//
// A process that is your own child and not yet reaped is a zombie, which
// [IsAlive] reports as alive; reap children (Wait) or Stop will time out.
// [Spawn] does this for you.
//
// The window between Verify and the signal is inherent to PID-based control
// and is not closed here.
func Stop(ctx context.Context, pid int, opts StopOptions) error {
	if pid <= 0 || pid == os.Getpid() {
		return fmt.Errorf("%w: refusing to stop pid %d", ErrInvalidPID, pid)
	}
	if opts.Verify == nil {
		return ErrVerifyRequired
	}
	opts = opts.withDefaults()
	if !IsAlive(pid) {
		return nil
	}
	err := verify(ctx, pid, opts)
	if err != nil {
		return err
	}
	if err = signalPID(pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("send SIGTERM to %d: %w", pid, err)
	}
	gone, err := waitGone(ctx, pid, opts.GracePeriod, opts.PollInterval)
	if err != nil || gone {
		return err
	}

	// Still alive after the grace period. The PID may have been recycled
	// meanwhile; do not SIGKILL a stranger.
	if err = verify(ctx, pid, opts); err != nil {
		if errors.Is(err, ErrIdentityMismatch) {
			return nil // the process we signaled is gone
		}
		return err
	}
	if err = signalPID(pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("send SIGKILL to %d: %w", pid, err)
	}
	gone, err = waitGone(ctx, pid, opts.KillTimeout, opts.PollInterval)
	if err != nil || gone {
		return err
	}
	return fmt.Errorf("%w: pid %d (grace %s, kill %s)", ErrStopTimeout, pid, opts.GracePeriod, opts.KillTimeout)
}

func verify(ctx context.Context, pid int, opts StopOptions) error {
	ok, err := opts.Verify(ctx, pid)
	if err != nil {
		return fmt.Errorf("verify pid %d: %w", pid, err)
	}
	if !ok {
		return fmt.Errorf("%w: pid %d is not the expected process; refusing to signal", ErrIdentityMismatch, pid)
	}
	return nil
}

// waitGone polls until pid is gone (true), timeout elapses (false), or ctx
// ends (error).
func waitGone(ctx context.Context, pid int, timeout, interval time.Duration) (bool, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if !IsAlive(pid) {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, fmt.Errorf("localdaemon: waiting for pid %d: %w", pid, ctx.Err())
		case <-deadline.C:
			return !IsAlive(pid), nil
		case <-tick.C:
		}
	}
}
