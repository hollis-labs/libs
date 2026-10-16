//go:build !linux && !darwin

package gitadapter

import "io/fs"

const readNonblockFlag = 0

func identity(fs.FileInfo) string    { return "" }
func safeDirectory(fs.FileInfo) bool { return false }
