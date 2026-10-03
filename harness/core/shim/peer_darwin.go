//go:build darwin

package shim

import "net"

// The capability challenge is mandatory on every supported host. Darwin's
// stdlib does not expose getpeereid; the hello capability reports this limit.
func checkPeer(c *net.UnixConn) error { return nil }
