//go:build linux || darwin

package local

import (
	"context"
	"errors"
	"github.com/hollis-labs/substrate/harness/workspace"
	"io/fs"
	"os"
	"sync"
	"syscall"
	"time"
)

type heldLock struct {
	file *os.File
	once sync.Once
	err  error
}

func (l *heldLock) Release() error {
	l.once.Do(func() { l.err = errors.Join(syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN), l.file.Close()) })
	return l.err
}
func (p *ports) acquireFile(ctx context.Context, name string) (workspace.HeldLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if info, err := p.control.Lstat(name); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return nil, errors.New("local: invalid lock file")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	file, err := p.control.OpenFile(name, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &heldLock{file: file}, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			file.Close()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
