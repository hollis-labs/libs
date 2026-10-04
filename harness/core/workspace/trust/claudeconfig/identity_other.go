//go:build !linux && !darwin

package claudeconfig

import (
	"io/fs"
	"os"
)

func safeDirectory(fs.FileInfo) bool { return false }
func safeFile(fs.FileInfo) bool      { return false }

func openConfigReadOnly(*os.Root, string) (*os.File, error) { return nil, errConfig }
