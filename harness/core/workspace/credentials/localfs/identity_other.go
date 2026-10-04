//go:build !linux && !darwin

package localfs

import "io/fs"

func identity(fs.FileInfo) string       { return "" }
func safeDirectory(fs.FileInfo) bool    { return false }
func privateDirectory(fs.FileInfo) bool { return false }
