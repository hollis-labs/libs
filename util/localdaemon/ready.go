package localdaemon

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// WaitReady polls check until it reports ready, an error, the deadline, or
// ctx ending. check runs immediately, then every pollInterval (default
// 100ms when zero or negative).
//
//   - check returns (true, nil): WaitReady returns nil.
//   - check returns (false, nil): not ready yet, keep polling.
//   - check returns an error: fatal, WaitReady stops at once and returns it
//     (use this for "the child already exited": there is no point waiting).
//   - deadline elapses: an error matching [ErrNotReady].
//   - ctx ends: an error matching ctx.Err().
//
// deadline must be positive. The context passed to check is bounded by the
// deadline, so a slow check cannot overrun it.
func WaitReady(ctx context.Context, deadline, pollInterval time.Duration, check func(context.Context) (bool, error)) error {
	if deadline <= 0 {
		return errors.New("localdaemon: WaitReady deadline must be positive")
	}
	if check == nil {
		return errors.New("localdaemon: WaitReady needs a check")
	}
	if pollInterval <= 0 {
		pollInterval = 100 * time.Millisecond
	}
	cctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	for {
		ready, err := check(cctx)
		if err != nil {
			return fmt.Errorf("localdaemon: readiness check failed: %w", err)
		}
		if ready {
			return nil
		}
		select {
		case <-cctx.Done():
			if perr := ctx.Err(); perr != nil {
				return fmt.Errorf("localdaemon: waiting for readiness: %w", perr)
			}
			return fmt.Errorf("%w (%s)", ErrNotReady, deadline)
		case <-tick.C:
		}
	}
}
