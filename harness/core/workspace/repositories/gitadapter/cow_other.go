//go:build !linux && !darwin

package gitadapter

import (
	"io/fs"
	"os"

	"github.com/hollis-labs/substrate/harness/workspace/repositories"
)

type platformCloner struct{}

func newCloner() cloner                                 { return platformCloner{} }
func (platformCloner) method() repositories.CloneMethod { return repositories.NoClone }
func (platformCloner) probe(*os.File, *os.File) (bool, string, error) {
	return false, "platform_unsupported", nil
}
func (platformCloner) clone(*os.File, *os.File, string, fs.FileMode) error { return errGit }
