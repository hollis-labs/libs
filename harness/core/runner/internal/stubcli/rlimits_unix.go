//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

func emitRlimit(name string, resource int) {
	var rlim syscall.Rlimit
	if err := syscall.Getrlimit(resource, &rlim); err != nil {
		fmt.Fprintf(os.Stdout, "{\"type\":\"delta\",\"content\":\"%s=err \"}\n", name)
		return
	}
	fmt.Fprintf(os.Stdout, "{\"type\":\"delta\",\"content\":\"%s=%d \"}\n", name, rlim.Cur)
}

func showResourceLimits() {
	emitRlimit("cpu", syscall.RLIMIT_CPU)
	emitRlimit("nofile", syscall.RLIMIT_NOFILE)
	emitRlimit("fsize", syscall.RLIMIT_FSIZE)
}
