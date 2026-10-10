package gitadapter

import (
	"io/fs"
	"os"

	"github.com/hollis-labs/substrate/harness/workspace/repositories"
)

// cloner is one file-level construction primitive. Production ports use the
// platform copy-on-write primitive only; a plain copy is never substituted.
type cloner interface {
	method() repositories.CloneMethod
	// probe clones src into a disposable owned fixture beneath dir and reports
	// support with a stable reason. An error means the result is unknown.
	probe(src, dir *os.File) (bool, string, error)
	// clone creates name exclusively in dir as a clone of src.
	clone(src, dir *os.File, name string, perm fs.FileMode) error
}
