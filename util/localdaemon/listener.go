package localdaemon

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// Listener opens the transport named by a scheme-prefixed address. This
// helper is optional and independent of the rest of the package.
//
//	unix:/absolute/path   Unix domain socket (Unix-like platforms only)
//	tcp:host:port         TCP; the caller chooses the interface, so use a
//	                      loopback address for a local control plane
//
// For unix: addresses the parent directory is created (0750), a leftover
// socket file from a crashed daemon is removed, and the new socket is set to
// mode 0600. A stale socket is removed only if the file is a socket AND
// nothing accepts a connection on it; a live one yields [ErrAlreadyRunning]
// and a non-socket file is refused untouched.
//
// That dial check is defense in depth, not mutual exclusion: two daemons
// starting at the same instant can both see "stale". Take the [Lock] first;
// with it held, removing the socket is unconditionally safe.
func Listener(addr string) (net.Listener, error) {
	switch {
	case strings.HasPrefix(addr, "unix:"):
		path := strings.TrimPrefix(addr, "unix:")
		if path == "" {
			return nil, errors.New("localdaemon: unix address missing path")
		}
		return listenUnix(path)
	case strings.HasPrefix(addr, "tcp:"):
		hostPort := strings.TrimPrefix(addr, "tcp:")
		if hostPort == "" {
			return nil, errors.New("localdaemon: tcp address missing host:port")
		}
		return net.Listen("tcp", hostPort)
	default:
		return nil, fmt.Errorf("localdaemon: unsupported address %q (want unix:/path or tcp:host:port)", addr)
	}
}

func listenUnix(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}
	if err := removeStaleSocket(path); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("chmod socket: %w", err)
	}
	return ln, nil
}
