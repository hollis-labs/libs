package secrets

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/user"
	"strings"

	"github.com/hollis-labs/go-secrets/credfile"
)

// FileBackend serves file:///abs/path and file://~/path from a private
// credential file, checked by package credfile: absolute, regular, owned by the
// caller, mode 0600 or tighter, symlinks confined, non-empty, at most 64 KiB.
//
// The value is whatever the reference says: ${VAR} is not expanded, and ~ is
// resolved through the user database (see [WithHome]), not $HOME.
type FileBackend struct {
	roots []string
	home  func() (string, error)
}

// Scheme implements Backend.
func (*FileBackend) Scheme() string { return "file" }

// Resolve implements Backend.
func (b *FileBackend) Resolve(_ context.Context, ref Ref) (Resolved, error) {
	if len(ref.Path) != 1 {
		return Resolved{}, newErr(KindInvalid, ref, "", "file references look like file:///abs/path or file://~/path", nil)
	}
	path := ref.Path[0]
	home, herr := b.homeDir()
	if path == "~" || strings.HasPrefix(path, "~/") {
		if herr != nil {
			return Resolved{}, newErr(KindUnavailable, ref, "file", "cannot resolve ~: no home directory", herr)
		}
		path = home + strings.TrimPrefix(path, "~")
	}
	roots := append([]string(nil), b.roots...)
	if herr == nil {
		roots = append(roots, home)
	}
	v, err := credfile.Read(path, credfile.Options{Roots: roots})
	if err != nil {
		return Resolved{}, classifyFile(ref, err)
	}
	return Resolved{Secret: FromString(v), Source: "file"}, nil
}

func (b *FileBackend) homeDir() (string, error) {
	if b.home != nil {
		return b.home()
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return u.HomeDir, nil
	}
	return os.UserHomeDir()
}

// classifyFile maps credfile errors onto the taxonomy. credfile's messages name
// the path and the rule that failed and never contain file contents, so they
// are used as the Reason.
func classifyFile(ref Ref, err error) error {
	reason := err.Error()
	switch {
	case errors.Is(err, credfile.ErrEmpty):
		return newErr(KindEmpty, ref, "file", reason, err)
	case errors.Is(err, credfile.ErrTooLarge):
		return newErr(KindTooLarge, ref, "file", reason, err)
	case errors.Is(err, credfile.ErrNotAbsolute), errors.Is(err, credfile.ErrNotRegular),
		errors.Is(err, credfile.ErrTooPermissive), errors.Is(err, credfile.ErrNotOwner),
		errors.Is(err, credfile.ErrSymlinkEscape):
		return newErr(KindInvalid, ref, "file", reason, err)
	case errors.Is(err, fs.ErrNotExist):
		return newErr(KindNotFound, ref, "file", reason, err)
	case errors.Is(err, fs.ErrPermission):
		return newErr(KindDenied, ref, "file", reason, err)
	}
	return newErr(KindUnavailable, ref, "file", "the credential file could not be read", err)
}
