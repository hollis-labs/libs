//go:build linux || darwin

package materialize

import (
	"fmt"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"golang.org/x/sys/unix"
	"os"
	"strings"
	"syscall"
)

func installedIdentity(info os.FileInfo) string {
	if info == nil {
		return ""
	}
	s, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d", s.Dev, s.Ino)
}
func openInstalledRegular(root *os.Root, p string) (*os.File, error) {
	if artifact.ValidateRelPath(p) != nil {
		return nil, ErrUnsafeTarget
	}
	// os.Root resolves confined links itself, including with O_NOFOLLOW. Walk
	// pinned descriptors instead, refusing symlinks at EVERY path component.
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	fd := int(directory.Fd())
	ownedFD := -1
	defer directory.Close()
	defer func() {
		if ownedFD >= 0 {
			unix.Close(ownedFD)
		}
	}()
	parts := strings.Split(p, "/")
	for _, part := range parts[:len(parts)-1] {
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		if ownedFD >= 0 {
			unix.Close(ownedFD)
		}
		fd = next
		ownedFD = next
	}
	leaf, err := unix.Openat(fd, parts[len(parts)-1], unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(leaf), p)
	if file == nil {
		unix.Close(leaf)
		return nil, ErrUnsafeTarget
	}
	return file, nil
}

// InstalledIdentity observes a stable file identity; it confers no ownership.
func InstalledIdentity(info os.FileInfo) string { return installedIdentity(info) }
