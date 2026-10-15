//go:build linux

package materialize

import "os"

func installedCreator() (uint32, uint32) { return uint32(os.Geteuid()), uint32(os.Getegid()) }

// ext4 may hide trusted.* entries from an unprivileged Flistxattr caller.
// Empty visible enumeration therefore cannot establish complete metadata.
// Until an independently reviewed producer can establish every namespace and
// creation inheritance, installed execution is unsupported on this backend.
// No privilege probe, namespace filter, metadata copier or caller boolean can
// turn partial coverage into an authority claim.
func installedMetadata(*os.File, bool) (InstalledMetadata, string, error) {
	return InstalledMetadata{}, "", ErrUnsupportedOperation
}
