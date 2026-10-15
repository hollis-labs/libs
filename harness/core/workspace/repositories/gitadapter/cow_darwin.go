//go:build darwin

package gitadapter

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"

	"github.com/hollis-labs/substrate/harness/workspace/repositories"
)

// platformCloner uses clonefile. The probe creates one randomly named owned
// fixture beneath the private attachment base and removes it. Real APFS
// support on this path is unmeasured in this repository's checks.
type platformCloner struct{}

const cloneFlags = unix.CLONE_NOFOLLOW | unix.CLONE_NOOWNERCOPY

func newCloner() cloner                                 { return platformCloner{} }
func (platformCloner) method() repositories.CloneMethod { return repositories.Clonefile }
func (platformCloner) probe(src, dir *os.File) (bool, string, error) {
	var b [12]byte
	if _, e := rand.Read(b[:]); e != nil {
		return false, "", e
	}
	name := ".clone-probe-" + hex.EncodeToString(b[:])
	switch e := unix.Fclonefileat(int(src.Fd()), int(dir.Fd()), name, cloneFlags); {
	case e == nil:
		if e = unix.Unlinkat(int(dir.Fd()), name, 0); e != nil {
			return false, "", e
		}
		return true, "", nil
	case errors.Is(e, unix.EXDEV):
		return false, "cross_filesystem", nil
	case errors.Is(e, unix.ENOTSUP) || errors.Is(e, unix.EOPNOTSUPP):
		return false, "clonefile_unsupported", nil
	default:
		return false, "", e
	}
}
func (platformCloner) clone(src, dir *os.File, name string, perm fs.FileMode) error {
	if e := unix.Fclonefileat(int(src.Fd()), int(dir.Fd()), name, cloneFlags); e != nil {
		return e
	}
	return unix.Fchmodat(int(dir.Fd()), name, uint32(perm.Perm()), unix.AT_SYMLINK_NOFOLLOW)
}
