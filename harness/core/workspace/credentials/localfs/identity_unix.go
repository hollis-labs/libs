//go:build linux || darwin

package localfs

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

func openSourceReadOnly(root *os.Root, rel string) (*os.File, error) {
	return root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

func identity(st fs.FileInfo) string {
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d", s.Dev, s.Ino)
}
func safeDirectory(st fs.FileInfo) bool {
	s, ok := st.Sys().(*syscall.Stat_t)
	return ok && st.IsDir() && st.Mode()&fs.ModeSymlink == 0 && s.Uid == uint32(os.Geteuid()) && st.Mode().Perm()&0022 == 0
}
func privateDirectory(st fs.FileInfo) bool { return safeDirectory(st) && st.Mode().Perm() == 0700 }
