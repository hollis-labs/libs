//go:build linux || darwin

package snapshot

import (
	"io/fs"
	"os"
	"syscall"
)

func ownedStore(info fs.FileInfo) bool {
	s, ok := info.Sys().(*syscall.Stat_t)
	return ok && s.Uid == uint32(os.Geteuid())
}
