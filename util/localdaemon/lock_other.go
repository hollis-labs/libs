//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package localdaemon

import "context"

// TryAcquire is not supported on this platform; it returns [ErrUnsupported].
func TryAcquire(string) (*Lock, error) { return nil, ErrUnsupported }

// Acquire is not supported on this platform; it returns [ErrUnsupported].
func Acquire(context.Context, string) (*Lock, error) { return nil, ErrUnsupported }

// SetInfo is not supported on this platform; it returns [ErrUnsupported].
func (l *Lock) SetInfo([]byte) error { return ErrUnsupported }

// Path returns the lock file path.
func (l *Lock) Path() string { return l.path }

// Release is a no-op on this platform, where no Lock can be acquired.
func (l *Lock) Release() error { return nil }
