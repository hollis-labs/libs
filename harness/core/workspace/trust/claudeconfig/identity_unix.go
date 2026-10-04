//go:build linux || darwin

package claudeconfig

import (
	"io/fs"
	"os"
	"syscall"
)

func safeDirectory(st fs.FileInfo) bool {
	s, ok := st.Sys().(*syscall.Stat_t)
	return ok && st.IsDir() && st.Mode()&fs.ModeSymlink == 0 && s.Uid == uint32(os.Geteuid()) && st.Mode().Perm()&0022 == 0 && st.Mode().Perm()&0300 == 0300
}
func safeFile(st fs.FileInfo) bool {
	s, ok := st.Sys().(*syscall.Stat_t)
	return ok && st.Mode().IsRegular() && s.Uid == uint32(os.Geteuid()) && st.Mode().Perm()&0022 == 0
}

func openConfigReadOnly(root *os.Root, rel string) (*os.File, error) {
	return root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
