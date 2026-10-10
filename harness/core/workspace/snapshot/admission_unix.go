//go:build linux || darwin

package snapshot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type admissionLock struct {
	owner  *Admission
	file   *os.File
	active bool
}

func privateAdmission(info os.FileInfo, dir bool) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() {
		return false
	}
	if dir {
		return info.Mode() == os.ModeDir|0700
	}
	return info.Mode() == 0600 && st.Nlink == 1
}
func openAdmission(name string, policy CapturePolicy) (*Admission, error) {
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return nil, ErrAdmissionUnavailable
	}
	// Verify every existing component without resolving a symlink into authority.
	for p := name; ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrAdmissionUnavailable
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	info, err := os.Lstat(name)
	if err != nil || !privateAdmission(info, true) {
		return nil, ErrAdmissionUnavailable
	}
	root, err := os.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	a := &Admission{root: name, directory: root, identity: info, policy: policy}
	if err = a.checkRoot(); err != nil {
		root.Close()
		return nil, err
	}
	return a, nil
}
func (a *Admission) checkRoot() error {
	opened, err := a.directory.Stat(".")
	if err != nil {
		return err
	}
	named, err := os.Lstat(a.root)
	if err != nil {
		return err
	}
	if !privateAdmission(opened, true) || !privateAdmission(named, true) || !os.SameFile(a.identity, opened) || !os.SameFile(opened, named) {
		return ErrAdmissionUnavailable
	}
	// An ancestry replacement, even when the held root itself survives, is not a
	// new grant. This supplements confinement; it does not claim isolation.
	for p := filepath.Dir(a.root); ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrAdmissionUnavailable
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return nil
}
func (a *Admission) checkFile(f *os.File, name string) error {
	if err := a.checkRoot(); err != nil {
		return err
	}
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	named, err := a.directory.Lstat(name)
	if err != nil {
		return err
	}
	if !privateAdmission(opened, false) || !privateAdmission(named, false) || !os.SameFile(opened, named) {
		return ErrAdmissionUnavailable
	}
	flags, _, e := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_GETFD, 0)
	if e != 0 {
		return e
	}
	if flags&syscall.FD_CLOEXEC == 0 {
		return ErrAdmissionUnavailable
	}
	return nil
}
func (a *Admission) lock(ctx context.Context) (*admissionLock, error) {
	if ctx == nil {
		return nil, ErrAdmissionUnavailable
	}
	if err := a.checkRoot(); err != nil {
		return nil, err
	}
	f, err := a.directory.OpenFile("admission.lock", os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	fail := func(e error) (*admissionLock, error) { return nil, errors.Join(e, f.Close()) }
	if err = a.checkFile(f, "admission.lock"); err != nil {
		return fail(err)
	}
	for {
		if err = ctx.Err(); err != nil {
			return fail(err)
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			h := &admissionLock{owner: a, file: f, active: true}
			if err = h.check(); err != nil {
				return nil, errors.Join(err, h.close())
			}
			return h, nil
		}
		if err != syscall.EAGAIN && err != syscall.EWOULDBLOCK {
			return fail(err)
		}
		t := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			t.Stop()
			return fail(ctx.Err())
		case <-t.C:
		}
	}
}
func (h *admissionLock) check() error {
	if h == nil || !h.active || h.file == nil {
		return ErrAdmissionUnavailable
	}
	return h.owner.checkFile(h.file, "admission.lock")
}
func (h *admissionLock) close() error {
	if h == nil || !h.active {
		return nil
	}
	h.active = false
	return errors.Join(syscall.Flock(int(h.file.Fd()), syscall.LOCK_UN), h.file.Close())
}
func (a *Admission) syncDirectory() error {
	f, err := a.directory.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

func (a *Admission) openLedger() (*os.File, error) {
	info, err := a.directory.Lstat("admission.json")
	if err != nil {
		return nil, err
	}
	if !privateAdmission(info, false) {
		return nil, ErrAdmissionUnavailable
	}
	return a.directory.OpenFile("admission.json", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

func admissionStoreIdentity(info os.FileInfo) string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d:%d", st.Dev, st.Ino, st.Uid)
}
