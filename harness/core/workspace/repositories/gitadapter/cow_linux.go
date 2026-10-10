//go:build linux

package gitadapter

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"

	"github.com/hollis-labs/substrate/harness/workspace/repositories"
)

// platformCloner uses FICLONE. Support is proved per filesystem pair by a
// clone into an unnamed O_TMPFILE inode: nothing is linked into the directory
// and the fixture disappears when closed.
type platformCloner struct{}

func newCloner() cloner                                 { return platformCloner{} }
func (platformCloner) method() repositories.CloneMethod { return repositories.Reflink }
func unsupported(e error) bool {
	return errors.Is(e, unix.EOPNOTSUPP) || errors.Is(e, unix.EINVAL) || errors.Is(e, unix.ENOTTY) || errors.Is(e, unix.ENOSYS) || errors.Is(e, unix.EISDIR)
}
func (platformCloner) probe(src, dir *os.File) (bool, string, error) {
	fd, e := unix.Openat(int(dir.Fd()), ".", unix.O_TMPFILE|unix.O_WRONLY|unix.O_CLOEXEC, 0600)
	if e != nil {
		if unsupported(e) {
			return false, "probe_fixture_unsupported", nil
		}
		return false, "", e
	}
	defer unix.Close(fd)
	switch e = unix.IoctlFileClone(fd, int(src.Fd())); {
	case e == nil:
		return true, "", nil
	case errors.Is(e, unix.EXDEV):
		return false, "cross_filesystem", nil
	case unsupported(e):
		return false, "reflink_unsupported", nil
	}
	return false, "", e
}
func (platformCloner) clone(src, dir *os.File, name string, perm fs.FileMode) error {
	fd, e := unix.Openat(int(dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(perm.Perm()))
	if e != nil {
		return e
	}
	e = unix.IoctlFileClone(fd, int(src.Fd()))
	if c := unix.Close(fd); e == nil {
		e = c
	}
	return e
}
