package local

import (
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/hollis-labs/substrate/harness/workspace"
)

// MutationMappingVersion names the existing local backend protocol. Namespace
// is the containing directory; ONLY raw CanonicalID bytes enter the hash.
const MutationMappingVersion = "workspace.local.mutation.v1"

// MutationLockPath explains the existing Acquire mapping without opening,
// creating or migrating an inode, or establishing canonical custody/authority.
func MutationLockPath(key workspace.LockKey) (string, error) {
	for _, value := range []string{key.Namespace, key.CanonicalID} {
		if !utf8.ValidString(value) || !filepath.IsAbs(value) || filepath.Clean(value) != value || len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
			return "", errors.New("local: invalid canonical mutation key")
		}
	}
	return filepath.Join(key.Namespace, "lock-"+digestName(key.CanonicalID)), nil
}
