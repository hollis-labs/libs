//go:build linux || darwin

package shim

import (
	"os"
	"syscall"
)

func openLock(path string, exclusive bool) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		f.Close()
		return nil, fault("unsafe_path", "lock must be a private regular file")
	}
	flags := syscall.LOCK_SH
	if exclusive {
		flags = syscall.LOCK_EX | syscall.LOCK_NB
	}
	if err = syscall.Flock(fd, flags); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, fault("in_use", "lock unavailable")
		}
		return nil, fault("lock_unknown", "lock state could not be established")
	}
	return f, nil
}

func openPrivateFile(path string, flags int) (*os.File, error) {
	fd, err := syscall.Open(path, flags|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		f.Close()
		return nil, fault("unsafe_path", "expected private regular file")
	}
	return f, nil
}

// A pin is created by the launcher. Creating a new inode here would not
// protect the generation that the launcher actually reserved.
func lockExistingPin(path string) (*os.File, error) {
	f, err := openPrivateFile(path, syscall.O_RDWR)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fault("lock_unknown", "pin shared hold unavailable")
	}
	return f, nil
}
func signalGroup(pid int, signal syscall.Signal) error {
	err := syscall.Kill(-pid, signal)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}
