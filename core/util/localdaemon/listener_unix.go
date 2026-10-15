//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package localdaemon

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"time"
)

// removeStaleSocket deletes path only if it is a socket nothing is listening
// on. See [Listener].
func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("localdaemon: %q exists and is not a socket", path)
	}
	if conn, derr := net.DialTimeout("unix", path, 200*time.Millisecond); derr == nil {
		_ = conn.Close()
		return fmt.Errorf("%w: a process is still accepting connections on %q", ErrAlreadyRunning, path)
	}
	return os.Remove(path)
}
