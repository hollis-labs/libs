//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package localdaemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// lockPollInterval is how often Acquire retries a held lock.
const lockPollInterval = 25 * time.Millisecond

// TryAcquire takes the exclusive lock at path without blocking, creating the
// file (mode 0600) and its parent directories if needed. If another holder
// has it, the error is a *[HeldError] (and matches [ErrAlreadyRunning]).
//
// On success the holder's PID is written to the file for diagnostics. Two
// TryAcquire calls on the same path conflict even inside one process.
func TryAcquire(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil { //nolint:gosec // G703: caller-chosen path is the API
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	// A retry is needed only if the path is replaced between open and lock.
	for range 5 {
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // G304: caller-chosen path is the API
		if err != nil {
			return nil, fmt.Errorf("open lock file: %w", err)
		}
		if err := flock(f, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			if errors.Is(err, syscall.EWOULDBLOCK) {
				held := &HeldError{Path: path}
				if b, rerr := os.ReadFile(path); rerr == nil { //nolint:gosec // G304: same caller-chosen path
					held.HolderPID, held.Info = parseHolder(b)
				}
				_ = f.Close()
				return nil, held
			}
			_ = f.Close()
			return nil, fmt.Errorf("flock %s: %w", path, err)
		}
		// We hold a lock, but on the inode we opened. If the path now names
		// a different inode (someone unlinked or replaced the file), that
		// lock protects nothing; start over.
		if !stillAtPath(f, path) {
			_ = f.Close()
			continue
		}
		l := &Lock{file: f, path: path}
		if err := l.write(nil); err != nil {
			_ = l.Release()
			return nil, err
		}
		return l, nil
	}
	return nil, fmt.Errorf("localdaemon: lock file %s kept being replaced", path)
}

// Acquire is [TryAcquire] that waits for a held lock, polling until it is
// free or ctx ends. The blocking form of flock(2) cannot be interrupted, so
// this polls instead; it is not FIFO-fair.
func Acquire(ctx context.Context, path string) (*Lock, error) {
	t := time.NewTicker(lockPollInterval)
	defer t.Stop()
	for {
		l, err := TryAcquire(path)
		if err == nil {
			return l, nil
		}
		var held *HeldError
		if !errors.As(err, &held) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("localdaemon: acquire %s: %w (last: %w)", path, ctx.Err(), err)
		case <-t.C:
		}
	}
}

// SetInfo replaces the caller-defined payload stored after the PID line in
// the lock file, for status displays. The package does not interpret it.
// Readers see it in [HeldError.Info].
func (l *Lock) SetInfo(info []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return errors.New("localdaemon: lock released")
	}
	return l.write(info)
}

// write replaces the file content with "<pid>\n" + info. Callers hold l.mu or
// own l exclusively.
func (l *Lock) write(info []byte) error {
	content := append([]byte(strconv.Itoa(os.Getpid())+"\n"), info...)
	if err := l.file.Truncate(0); err != nil {
		return fmt.Errorf("write lock info: %w", err)
	}
	if _, err := l.file.WriteAt(content, 0); err != nil {
		return fmt.Errorf("write lock info: %w", err)
	}
	return nil
}

// Path returns the lock file path.
func (l *Lock) Path() string { return l.path }

// Release drops the lock and closes the file, leaving the file on disk. It
// is safe to call more than once and on a nil Lock.
func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	_ = flock(l.file, syscall.LOCK_UN)
	err := l.file.Close()
	l.file = nil
	return err
}

func flock(f *os.File, how int) error {
	for {
		err := syscall.Flock(int(f.Fd()), how) //nolint:gosec // fd fits in int
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

func stillAtPath(f *os.File, path string) bool {
	a, err := f.Stat()
	if err != nil {
		return false
	}
	b, err := os.Stat(path) //nolint:gosec // G703: same caller-chosen path
	if err != nil {
		return false
	}
	return os.SameFile(a, b)
}
