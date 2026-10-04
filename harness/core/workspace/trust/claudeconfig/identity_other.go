//go:build !linux && !darwin

package claudeconfig

import "io/fs"

func safeDirectory(fs.FileInfo) bool { return false }
func safeFile(fs.FileInfo) bool      { return false }
