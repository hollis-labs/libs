//go:build !linux && !darwin

package localfs

import (
	"io/fs"
	"os"
)

func openSourceReadOnly(*os.Root, string) (*os.File, error) { return nil, ErrUnsupported }

func identity(fs.FileInfo) string       { return "" }
func safeDirectory(fs.FileInfo) bool    { return false }
func privateDirectory(fs.FileInfo) bool { return false }
