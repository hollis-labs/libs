//go:build linux || darwin

package shim

import (
	"syscall"
	"unsafe"
)

// waitLeaderExit observes exit without reaping. Keeping the leader PID reserved
// allows final group cleanup without signalling a potentially reused PID/group.
func waitLeaderExit(pid int) error {
	var info [32]uint64
	for {
		_, _, err := syscall.Syscall6(syscall.SYS_WAITID, 1, uintptr(pid), uintptr(unsafe.Pointer(&info[0])), syscall.WEXITED|syscall.WNOWAIT, 0, 0)
		if err == syscall.EINTR {
			continue
		}
		if err != 0 {
			return err
		}
		return nil
	}
}
