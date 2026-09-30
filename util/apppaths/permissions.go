package paths

import (
	"errors"
	"io/fs"
	"os"
	"runtime"
)

const (
	// DirMode is the mode go-apppaths forces onto every directory it owns:
	// the four base roots, the workspace directory and the workspace database
	// directory.
	DirMode os.FileMode = 0o700
	// FileMode is the mode go-apppaths forces onto every file it writes,
	// currently the persisted active-workspace pointer.
	FileMode os.FileMode = 0o600
)

// modesSupported reports whether the platform has POSIX mode semantics worth
// enforcing. Windows maps os.Chmod onto the read-only attribute alone, so
// 0700/0600 is neither achievable nor meaningful there. runtime.GOOS is a
// constant, so this folds away at compile time.
const modesSupported = runtime.GOOS != "windows"

// ensureOwnedDir creates dir (and any missing parents) and then forces DirMode
// onto dir itself. The chmod is load-bearing: MkdirAll's mode is masked by the
// umask on creation and ignored entirely when dir already exists, so without
// it a directory left at 0755 by an older release would stay that way forever.
func ensureOwnedDir(dir string) error {
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return err
	}
	return chmodOwned(dir, DirMode)
}

// chmodOwned forces mode onto path, best effort, and never follows a symlink.
//
//   - A symlink is left alone: chmod would follow it to a target outside the
//     app's own tree, which the app may not own and did not create.
//   - A path the process may not chmod (owned by another user, read-only
//     mount) is left as it is rather than failing Resolve. Refusing to start
//     over a directory we cannot tighten would be a worse outcome than
//     leaving it as we found it.
//   - On Windows it does nothing.
func chmodOwned(path string, mode os.FileMode) error {
	if !modesSupported {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil
	}
	if info.Mode().Perm() == mode {
		return nil
	}
	if err := os.Chmod(path, mode); err != nil && !errors.Is(err, fs.ErrPermission) {
		return err
	}
	return nil
}
