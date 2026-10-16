//go:build !linux

package materialize

import "os"

func installedCreator() (uint32, uint32) { return 0, 0 }

// No unproved Darwin ACL/flags/enumeration or volume semantics are inferred
// from successful cross-compilation or another platform's observations.
func installedMetadata(*os.File, bool) (InstalledMetadata, string, error) {
	return InstalledMetadata{}, "", ErrUnsupportedOperation
}
