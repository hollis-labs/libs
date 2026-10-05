//go:build linux || darwin

package local

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func privateNative(info os.FileInfo, directory bool) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return false
	}
	if directory {
		return info.Mode() == os.ModeDir|0700
	}
	return info.Mode() == 0600 && stat.Nlink == 1
}

// Validate the already-opened root against the exact configured named root.
// This does not claim protection against noncooperating ancestor replacement.
func (p *ports) validateControlCustody() error {
	opened, err := p.control.Stat(".")
	if err != nil {
		return err
	}
	named, err := os.Lstat(p.options.ControlRoot.Path)
	if err != nil {
		return err
	}
	if !privateNative(opened, true) || !privateNative(named, true) || !os.SameFile(opened, named) {
		return errors.New("local: configured control custody changed")
	}
	return nil
}

// Binding is checked after acquisition as well as before it. No chmod, truncate,
// replacement or unlink repairs an unsafe resource into an acceptable one.
func (p *ports) validateFileCustody(file *os.File, name string) error {
	if err := p.validateControlCustody(); err != nil {
		return err
	}
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	named, err := p.control.Lstat(name)
	if err != nil {
		return err
	}
	configured, err := os.Lstat(filepath.Join(p.options.ControlRoot.Path, name))
	if err != nil {
		return err
	}
	if !privateNative(opened, false) || !privateNative(named, false) || !privateNative(configured, false) || !os.SameFile(opened, named) || !os.SameFile(opened, configured) {
		return errors.New("local: opened lock custody changed")
	}
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, file.Fd(), syscall.F_GETFD, 0)
	if errno != 0 {
		return errno
	}
	if flags&syscall.FD_CLOEXEC == 0 {
		return errors.New("local: lock descriptor is inheritable")
	}
	return p.validateControlCustody()
}
