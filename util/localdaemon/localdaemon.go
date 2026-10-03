package localdaemon

import (
	"errors"
	"fmt"
	"runtime"
)

// Sentinel errors. Match them with errors.Is; the errors returned by this
// package wrap them with context (paths, PIDs).
var (
	// ErrAlreadyRunning means another live instance was detected: a PID file
	// naming a live process, or a control socket something is still
	// accepting connections on.
	ErrAlreadyRunning = errors.New("localdaemon: already running")

	// ErrInvalidPID means a PID that must never be signaled or recorded: zero
	// or negative (kill(2) treats those as process groups), or the calling
	// process itself where that would be wrong.
	ErrInvalidPID = errors.New("localdaemon: invalid pid")

	// ErrCorruptPIDFile means a PID file exists but does not hold exactly one
	// positive integer. Callers usually treat it as stale.
	ErrCorruptPIDFile = errors.New("localdaemon: corrupt pid file")

	// ErrIdentityMismatch means Stop's Verify hook reported that the PID does
	// not belong to the expected program (typically PID reuse). Nothing was
	// signaled.
	ErrIdentityMismatch = errors.New("localdaemon: process identity mismatch")

	// ErrVerifyRequired means Stop was called without a Verify hook. Stop
	// never signals a PID it has not been given a way to identify.
	ErrVerifyRequired = errors.New("localdaemon: StopOptions.Verify is required")

	// ErrStopTimeout means the process was still present after SIGTERM, the
	// grace period, SIGKILL and the kill timeout.
	ErrStopTimeout = errors.New("localdaemon: process did not exit")

	// ErrNotReady means WaitReady's deadline elapsed before the check
	// reported ready.
	ErrNotReady = errors.New("localdaemon: not ready before deadline")
)

// ErrUnsupported is returned by TryAcquire, Acquire, VerifyCommand, Spawn,
// Stop and unix: Listener addresses on platforms without an implementation
// (see the README support table). It wraps [errors.ErrUnsupported].
var ErrUnsupported = fmt.Errorf("localdaemon: not supported on %s: %w", runtime.GOOS, errors.ErrUnsupported)
