//go:build linux || darwin

package materialize

import (
	"fmt"
	"os"
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
	return root.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}

// InstalledIdentity observes a stable file identity; it confers no ownership.
func InstalledIdentity(info os.FileInfo) string { return installedIdentity(info) }
