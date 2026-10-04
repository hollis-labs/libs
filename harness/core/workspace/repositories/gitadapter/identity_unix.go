//go:build linux || darwin

package gitadapter

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

func identity(st fs.FileInfo) string {
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d", s.Dev, s.Ino)
}
func safeDirectory(st fs.FileInfo) bool {
	s, ok := st.Sys().(*syscall.Stat_t)
	return ok && st.IsDir() && s.Uid == uint32(os.Getuid()) && st.Mode().Perm()&0022 == 0
}
