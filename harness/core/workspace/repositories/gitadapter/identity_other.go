//go:build !linux && !darwin

package gitadapter

import "io/fs"

func identity(fs.FileInfo) string    { return "" }
func safeDirectory(fs.FileInfo) bool { return false }
