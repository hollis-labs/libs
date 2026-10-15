//go:build darwin

package procuse

import (
	"strings"
	"syscall"
)

func scannableFilesystem(path string) bool {
	var stat syscall.Statfs_t
	if syscall.Statfs(path, &stat) != nil {
		return false
	}
	var name []byte
	for _, c := range stat.Fstypename {
		if c == 0 {
			break
		}
		name = append(name, byte(c))
	}
	switch strings.ToLower(string(name)) {
	case "devfs", "procfs", "sysfs", "fdescfs":
		return false
	}
	return true
}
