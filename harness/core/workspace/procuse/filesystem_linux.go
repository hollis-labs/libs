//go:build linux

package procuse

import "syscall"

func scannableFilesystem(path string) bool {
	var stat syscall.Statfs_t
	if syscall.Statfs(path, &stat) != nil {
		return false
	}
	switch uint64(stat.Type) {
	case 0x9fa0, 0x62656572, 0x1cd1, 0x1373, 0xcafe4a11, 0x64626720, 0x74726163, 0x73636673, 0x27e0eb, 0x63677270:
		// procfs, sysfs, devpts/devfs, bpf, debug/trace/security and cgroup.
		return false
	}
	return true
}
