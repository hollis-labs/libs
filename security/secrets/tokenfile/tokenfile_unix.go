//go:build unix

package tokenfile

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

var errUnsupported = errors.New("unsupported")

// openNoFollow opens read-only without following a final-component symlink and
// without blocking on a FIFO.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) //nolint:gosec // G304: caller-configured token path, checked after open
}

// createExclusive creates a new 0600 file, failing if it exists, never
// following a symlink.
func createExclusive(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, FileMode) //nolint:gosec // G304: caller-configured token path
}

func ownedByCurrentUser(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == currentUID()
}

// currentUID is a variable so a test can stand in for a file owned by someone else.
var currentUID = os.Geteuid

// stampOf captures what makes a watcher re-read: content (mtime, size, inode)
// and trust (mode, owner), so a chmod that loosens the file takes effect at once.
func stampOf(path string) stamp {
	fi, err := os.Lstat(path)
	if err != nil {
		return stamp{}
	}
	s := stamp{exists: true, modTime: fi.ModTime().UnixNano(), size: fi.Size(), mode: fi.Mode()}
	if sys, ok := fi.Sys().(*syscall.Stat_t); ok {
		s.ino = uint64(sys.Ino) //nolint:unconvert // width differs by platform
		s.uid = sys.Uid
	}
	return s
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil { //nolint:gosec // G304
		_ = d.Sync()
		_ = d.Close()
	}
}
