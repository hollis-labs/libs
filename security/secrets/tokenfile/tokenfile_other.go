//go:build !unix

package tokenfile

import (
	"errors"
	"io/fs"
	"os"
)

var errUnsupported = errors.New("unsupported")

func openNoFollow(string) (*os.File, error) { return nil, errUnsupported }

func createExclusive(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, FileMode) //nolint:gosec // G304
}

func ownedByCurrentUser(fs.FileInfo) bool { return false }

func stampOf(path string) stamp {
	fi, err := os.Lstat(path)
	if err != nil {
		return stamp{}
	}
	return stamp{exists: true, modTime: fi.ModTime().UnixNano(), size: fi.Size(), mode: fi.Mode()}
}

func syncDir(string) {}
