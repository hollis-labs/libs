//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package localdaemon

import (
	"errors"
	"syscall"
)

// IsAlive reports whether a process with this PID exists, using signal 0.
// A process owned by another user (EPERM) exists and counts as alive. A
// zombie that its parent has not reaped also counts as alive.
//
// IsAlive does NOT verify identity: a recycled PID looks alive. Pair it with
// [VerifyCommand] before signaling a PID read from a file that could outlive
// the process it named.
func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// signalPID sends sig to exactly one process. Callers must have rejected
// pid <= 0 already: kill(2) treats 0 and negatives as process groups.
func signalPID(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}
