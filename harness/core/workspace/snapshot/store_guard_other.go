//go:build !linux && !darwin

package snapshot

import "io/fs"

func ownedStore(_ fs.FileInfo) bool { return false }
