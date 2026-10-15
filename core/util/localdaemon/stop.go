package localdaemon

import (
	"context"
	"time"
)

// StopOptions configures [Stop].
type StopOptions struct {
	// GracePeriod is how long to wait for exit after SIGTERM before sending
	// SIGKILL. Zero means 5s.
	GracePeriod time.Duration
	// KillTimeout is how long to wait for exit after SIGKILL before giving
	// up with [ErrStopTimeout]. Zero means 1s.
	KillTimeout time.Duration
	// PollInterval is how often liveness is polled. Zero means 100ms.
	PollInterval time.Duration
	// Verify confirms that pid still belongs to the daemon. It is REQUIRED:
	// Stop returns [ErrVerifyRequired] without it. Stop signals nothing
	// unless Verify returns (true, nil); (false, nil) yields
	// [ErrIdentityMismatch] and an error is returned as is. Verify runs again
	// before SIGKILL, because the PID can be recycled during the grace
	// period. Typically it wraps [VerifyCommand]; a caller that knows the PID
	// is its own child may return true.
	Verify func(ctx context.Context, pid int) (bool, error)
}

// DefaultStopOptions returns the conventional timeouts: 5s grace, 1s kill
// timeout, 100ms poll. Verify is left nil and must be set.
func DefaultStopOptions() StopOptions {
	return StopOptions{
		GracePeriod:  5 * time.Second,
		KillTimeout:  1 * time.Second,
		PollInterval: 100 * time.Millisecond,
	}
}

func (o StopOptions) withDefaults() StopOptions {
	d := DefaultStopOptions()
	if o.GracePeriod <= 0 {
		o.GracePeriod = d.GracePeriod
	}
	if o.KillTimeout <= 0 {
		o.KillTimeout = d.KillTimeout
	}
	if o.PollInterval <= 0 {
		o.PollInterval = d.PollInterval
	}
	return o
}
