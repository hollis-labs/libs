package secrets

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/hollis-labs/libs/security/secrets/credfile"
	"github.com/hollis-labs/libs/security/secrets/internal/atomicfile"
)

// FileBackend serves file:///abs/path and file://~/path from a private
// credential file, checked by package credfile: absolute, regular, owned by the
// caller, mode 0600 or tighter, symlinks confined, non-empty, at most 64 KiB.
//
// The value is whatever the reference says: ${VAR} is not expanded, and ~ is
// resolved through the user database (see [WithHome]), not $HOME.
//
// A reference whose rest is not a path (file://<namespace>/<key>, no leading
// "/" or "~") names an entry in the managed file store under [StateDir]. That
// form is also writable: see [FileBackend.Set]. It is the default store on a
// host without a Secret Service.
type FileBackend struct {
	roots    []string
	home     func() (string, error)
	stateDir func() (string, error)
}

// Scheme implements Backend.
func (*FileBackend) Scheme() string { return "file" }

// Resolve implements Backend.
func (b *FileBackend) Resolve(_ context.Context, ref Ref) (Resolved, error) {
	if len(ref.Path) != 1 {
		return Resolved{}, newErr(KindInvalid, ref, "", "file references look like file:///abs/path or file://~/path", nil)
	}
	path := ref.Path[0]
	if isManaged(path) {
		return b.resolveManaged(ref)
	}
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

// isManaged reports whether the rest of a file reference names a managed entry
// rather than a filesystem path.
func isManaged(rest string) bool {
	return rest != "" && !strings.HasPrefix(rest, "/") && rest != "~" && !strings.HasPrefix(rest, "~/")
}

// StateDir is where the managed file store lives: $XDG_STATE_HOME (default
// ~/.local/state) + "/hollis/secrets". It is the layout go-apppaths resolves for
// the app name "hollis" (StateDir() + "/secrets"), without depending on it.
func StateDir() (string, error) {
	base := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", errors.New("no home directory and XDG_STATE_HOME is unset")
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "hollis", "secrets"), nil
}

func (b *FileBackend) root() (string, error) {
	if b.stateDir != nil {
		return b.stateDir()
	}
	return StateDir()
}

// managedPath validates file://<ns>/<key>[/...] and returns the entry's path.
// Every segment is a plain name: letters, digits, '.', '_', '-', '@', '+', ':'
// and not starting with '.', so no segment can climb out of the store.
func (b *FileBackend) managedPath(ref Ref) (root, path string, err error) {
	segs := strings.Split(ref.Path[0], "/")
	if len(segs) < 2 {
		return "", "", newErr(KindInvalid, ref, "file", "managed file references look like file://<namespace>/<key>", nil)
	}
	for _, s := range segs {
		if !validSegment(s) {
			return "", "", newErr(KindInvalid, ref, "file", "a managed file reference segment may use letters, digits and . _ - @ + : only, and may not start with '.'", nil)
		}
	}
	root, err = b.root()
	if err != nil {
		return "", "", newErr(KindUnavailable, ref, "file", "cannot locate the state directory", err)
	}
	return root, filepath.Join(append([]string{root}, segs...)...), nil
}

func validSegment(s string) bool {
	if s == "" || len(s) > 128 || s[0] == '.' {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-', r == '@', r == '+', r == ':':
		default:
			return false
		}
	}
	return true
}

func (b *FileBackend) resolveManaged(ref Ref) (Resolved, error) {
	root, path, err := b.managedPath(ref)
	if err != nil {
		return Resolved{}, err
	}
	v, err := credfile.Read(path, credfile.Options{Roots: []string{root}})
	if err != nil {
		return Resolved{}, classifyFile(ref, err)
	}
	return Resolved{Secret: FromString(v), Source: "file"}, nil
}

// Set implements Writer for managed references. The entry is written 0600 in a
// 0700 directory, atomically (temp file, fsync, rename), so a reader sees the
// old value or the new one. Path-form references (file:///abs) are not
// writable: this package does not decide where an operator's own file lives.
func (b *FileBackend) Set(_ context.Context, ref Ref, value Secret) error {
	if len(ref.Path) != 1 || !isManaged(ref.Path[0]) {
		return newErr(KindInvalid, ref, "file", "only managed file://<namespace>/<key> references can be written", nil)
	}
	root, path, err := b.managedPath(ref)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, atomicfile.DirMode); err != nil {
		return newErr(KindUnavailable, ref, "file", "cannot create the secrets directory", err)
	}
	if err := ensurePrivateDirs(root, path); err != nil {
		return newErr(KindUnavailable, ref, "file", "cannot create the secrets directory", err)
	}
	if err := atomicfile.Write(path, []byte(value.Reveal()+"\n")); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return newErr(KindDenied, ref, "file", "cannot write the secret file", err)
		}
		return newErr(KindUnavailable, ref, "file", "cannot write the secret file", err)
	}
	return nil
}

// Delete implements Writer. A missing entry is reported as not found.
func (b *FileBackend) Delete(_ context.Context, ref Ref) error {
	if len(ref.Path) != 1 || !isManaged(ref.Path[0]) {
		return newErr(KindInvalid, ref, "file", "only managed file://<namespace>/<key> references can be deleted", nil)
	}
	_, path, err := b.managedPath(ref)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return newErr(KindNotFound, ref, "file", "no such entry", err)
		}
		return newErr(KindUnavailable, ref, "file", "cannot remove the secret file", err)
	}
	return nil
}

// ensurePrivateDirs creates every directory between root and path's parent with
// mode 0700 and tightens the ones it finds: the store is private to the user.
func ensurePrivateDirs(root, path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, atomicfile.DirMode); err != nil {
		return err
	}
	for d := dir; len(d) >= len(root); d = filepath.Dir(d) {
		if fi, err := os.Lstat(d); err == nil && fi.IsDir() && fi.Mode().Perm() != atomicfile.DirMode {
			if err := os.Chmod(d, atomicfile.DirMode); err != nil {
				return err
			}
		}
		if d == root {
			break
		}
	}
	return nil
}
