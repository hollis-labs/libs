//go:build linux || darwin

package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func openCaptureFile(root *os.Root, path string) (*os.File, error) {
	// O_NOFOLLOW on the leaf alone does not prevent a swapped parent symlink.
	// Walk every directory using descriptor-relative, no-follow native opens.
	if filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrCoverageUnsupported
	}
	parts := strings.Split(path, string(filepath.Separator))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, ErrCoverageUnsupported
		}
	}
	parent, err := root.OpenFile(".", os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrCoverageUnsupported
	}
	defer func() { _ = parent.Close() }()
	for _, component := range parts[:len(parts)-1] {
		fd, err := unix.Openat(int(parent.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, ErrCoverageUnsupported
		}
		next := os.NewFile(uintptr(fd), component)
		_ = parent.Close()
		parent = next
	}
	fd, err := unix.Openat(int(parent.Fd()), parts[len(parts)-1], unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrCoverageUnsupported
	}
	return os.NewFile(uintptr(fd), parts[len(parts)-1]), nil
}
