//go:build linux

package shim

import (
	"net"
	"os"
	"syscall"
)

func checkPeer(c *net.UnixConn) error {
	raw, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var peerErr error
	err = raw.Control(func(fd uintptr) {
		cred, e := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if e != nil {
			peerErr = e
			return
		}
		if cred.Uid != uint32(os.Getuid()) {
			peerErr = fault("unauthorized", "peer UID mismatch")
		}
	})
	if err != nil {
		return err
	}
	return peerErr
}
