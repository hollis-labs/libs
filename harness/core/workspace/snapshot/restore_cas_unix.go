//go:build linux || darwin

package snapshot

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
)

func openRestoreParent(root *os.Root, path string) (*os.File, error) {
	if path != "." && !safeCapturedPath(path) {
		return nil, ErrCoverageUnsupported
	}
	fd, e := root.OpenFile(".", os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if e != nil {
		return nil, ErrCoverageUnsupported
	}
	if path == "." {
		return fd, nil
	}
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		next, e := unix.Openat(int(fd.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		fd.Close()
		if e != nil {
			return nil, ErrCoverageUnsupported
		}
		fd = os.NewFile(uintptr(next), part)
	}
	return fd, nil
}
func stageRestoreFile(f *restoreFile) error {
	fd, e := unix.Openat(int(f.parent.Fd()), f.stage, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if e != nil {
		return ErrCoverageUnsupported
	}
	out := os.NewFile(uintptr(fd), f.stage)
	n, e := out.Write(f.captured.Bytes)
	if e == nil && n != len(f.captured.Bytes) {
		e = ErrRestoreUncertain
	}
	if e == nil {
		e = out.Chmod(f.captured.Mode)
	}
	if e == nil {
		e = out.Sync()
	}
	closeErr := out.Close()
	if e != nil || closeErr != nil {
		return ErrRestoreUncertain
	}
	return nil
}
