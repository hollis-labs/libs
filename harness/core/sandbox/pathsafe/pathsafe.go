package pathsafe

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var _ error = (*EscapeError)(nil)

// EscapeError is returned by ResolveUnder when a resolved path falls outside
// the root. Root, Attempt and Resolved carry the inputs and the resolved
// location for diagnostics; Cause, when non-nil, is the underlying error.
type EscapeError struct {
	Root     string
	Attempt  string
	Resolved string
	Cause    error
}

// Error reports the attempted path and the root it escapes, including the
// resolved path and the underlying cause when they are known.
func (e *EscapeError) Error() string {
	base := fmt.Sprintf("pathsafe: %q escapes root %q", e.Attempt, e.Root)
	if e.Resolved != "" && e.Resolved != e.Attempt {
		base += fmt.Sprintf(" (resolved to %q)", e.Resolved)
	}
	if e.Cause != nil {
		base += ": " + e.Cause.Error()
	}
	return base
}

// Unwrap returns the underlying cause, if any, so errors.Is and errors.As
// see through an *EscapeError.
func (e *EscapeError) Unwrap() error { return e.Cause }

// ResolveUnder cleans userPath, joins it under root, and returns an absolute
// path guaranteed to live under root. It returns an *EscapeError if the
// resolved path falls outside root.
//
// Symlink handling:
//   - If the fully-joined path exists, filepath.EvalSymlinks is used to
//     resolve the full chain. A symlink pointing outside root fails.
//   - If the leaf does not exist, the longest existing ancestor is resolved
//     via EvalSymlinks and the non-existent suffix is re-joined. This lets
//     callers compute a safe target for a file they are about to create.
//   - Symlinks that stay within root are allowed.
//
// Other rules:
//   - Null bytes in userPath are rejected.
//   - userPath may be relative or absolute; absolute values are treated as
//     root-relative (the leading separator is stripped before joining).
//   - The root is also EvalSymlinks'd so that a symlinked root still compares
//     cleanly against resolved children.
func ResolveUnder(root, userPath string) (string, error) {
	if strings.ContainsRune(userPath, 0) {
		return "", &EscapeError{
			Root:    root,
			Attempt: userPath,
			Cause:   errors.New("null byte in path"),
		}
	}
	if root == "" {
		return "", errors.New("pathsafe: empty root")
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("pathsafe: abs root: %w", err)
	}
	// Resolve symlinks in the root where possible. If the root itself does
	// not exist, resolve through the longest existing ancestor so symlinked
	// prefixes (e.g. /var -> /private/var on macOS) are normalized.
	resolvedRoot, err := resolveSymlinksBestEffort(absRoot)
	if err != nil {
		return "", fmt.Errorf("pathsafe: resolve root: %w", err)
	}
	absRoot = filepath.Clean(resolvedRoot)

	// Treat absolute userPath values as root-relative.
	cleanUser := filepath.Clean(userPath)
	if filepath.IsAbs(cleanUser) {
		// Strip the leading separator (and volume on windows — filepath.Rel
		// handles volume, but we want a simple strip here).
		cleanUser = strings.TrimPrefix(cleanUser, string(filepath.Separator))
	}

	joined := filepath.Join(absRoot, cleanUser)
	resolved, err := resolveSymlinksBestEffort(joined)
	if err != nil {
		return "", &EscapeError{
			Root:     absRoot,
			Attempt:  userPath,
			Resolved: joined,
			Cause:    err,
		}
	}

	if !isUnder(absRoot, resolved) {
		return "", &EscapeError{
			Root:     absRoot,
			Attempt:  userPath,
			Resolved: resolved,
			Cause:    errors.New("resolved path outside root"),
		}
	}
	return resolved, nil
}

// resolveSymlinksBestEffort walks upward from p until it finds an existing
// ancestor, EvalSymlinks's it, and rejoins the non-existent suffix. This is
// the standard technique for validating a target path that does not yet
// exist (e.g., a file the caller is about to create).
func resolveSymlinksBestEffort(p string) (string, error) {
	return resolveSymlinksDepth(filepath.Clean(p), 0)
}

// maxSymlinkFollow bounds how many dangling links resolveSymlinksDepth follows
// by hand, so a chain of dangling links cannot loop.
const maxSymlinkFollow = 40

func resolveSymlinksDepth(p string, depth int) (string, error) {
	if evald, err := filepath.EvalSymlinks(p); err == nil {
		return evald, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}

	// EvalSymlinks failed with not-exist. If p ITSELF is a symlink, it is a
	// dangling one: the upward walk below starts at p's parent and would never
	// look at p, so a link whose target is a not-yet-existing path outside the
	// root would be reported as "root/link" and a caller creating that path would
	// write through it. Follow it by hand and resolve where it points instead.
	if info, err := os.Lstat(p); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if depth >= maxSymlinkFollow {
			return "", fmt.Errorf("too many symlinks resolving %q", p)
		}
		target, err := os.Readlink(p)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			// A relative target is relative to the link's REAL directory.
			realDir, err := filepath.EvalSymlinks(filepath.Dir(p))
			if err != nil {
				return "", err
			}
			target = filepath.Join(realDir, target)
		}
		return resolveSymlinksDepth(filepath.Clean(target), depth+1)
	}

	// Walk upward to find the longest existing ancestor.
	dir := p
	var suffix []string
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			// Hit the root (/ or C:\). Nothing to resolve; return cleaned.
			return p, nil
		}
		suffix = append([]string{filepath.Base(dir)}, suffix...)
		dir = parent
		if info, err := os.Lstat(dir); err == nil {
			if info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
				evald, err := filepath.EvalSymlinks(dir)
				if err != nil {
					return "", err
				}
				return filepath.Join(append([]string{evald}, suffix...)...), nil
			}
			// A non-dir exists at the ancestor path — path is malformed.
			return "", fmt.Errorf("ancestor %q is not a directory", dir)
		}
	}
}

// isUnder reports whether child lives at or under parent, using cleaned
// absolute paths. It is stricter than strings.HasPrefix: "/foo" is not under
// "/foobar".
func isUnder(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	if parent == child {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if strings.HasPrefix(rel, "..") {
		return false
	}
	if filepath.IsAbs(rel) {
		return false
	}
	return true
}
