package worktree

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Placement maps a caller id to a worktree path and back. It must be pure:
// Path and ID may not consult git or mutate anything, which is what lets an
// application derive a path from (repoRoot, id) without persisting it.
//
// ID must invert Path: for every valid id, ID(repo, Path(repo, id)) returns
// (id, true), and paths the placement did not produce return ok=false.
type Placement interface {
	Path(repoRoot, id string) string
	ID(repoRoot, path string) (id string, ok bool)
}

// Enumerator is an optional Placement extension. Candidates returns the
// on-disk paths that could hold a worktree of this placement. [Manager.Sweep]
// uses it to report leftovers that git no longer lists ([Report.Unregistered]);
// it never deletes them.
type Enumerator interface {
	Candidates(repoRoot string) []string
}

// Sibling places a worktree beside the repository, at the same directory
// depth: <parent>/<repoName>-worktrees-<prefix><id>. Same depth is what keeps
// relative `replace ../x` directives in go.mod resolving identically from the
// worktree and from the repository.
func Sibling(prefix string) Placement { return sibling{prefix: prefix} }

// UnderRoot places a worktree directly under root: <root>/<prefix><id>. A
// relative root is resolved against the repository root.
func UnderRoot(root, prefix string) Placement { return underRoot{root: root, prefix: prefix} }

// Nested places a worktree at <root>/<id>/<leaf>, leaving <root>/<id> free for
// per-session bookkeeping next to the checkout. A relative root is resolved
// against the repository root.
func Nested(root, leaf string) Placement { return nested{root: root, leaf: leaf} }

var (
	_ Placement  = sibling{}
	_ Placement  = underRoot{}
	_ Placement  = nested{}
	_ Enumerator = sibling{}
	_ Enumerator = underRoot{}
	_ Enumerator = nested{}
)

type sibling struct{ prefix string }

func (s sibling) stem(repoRoot string) string {
	return filepath.Base(filepath.Clean(repoRoot)) + "-worktrees-" + s.prefix
}

func (s sibling) Path(repoRoot, id string) string {
	repoRoot = filepath.Clean(repoRoot)
	return filepath.Join(filepath.Dir(repoRoot), s.stem(repoRoot)+id)
}

func (s sibling) ID(repoRoot, path string) (string, bool) {
	cr, cp := canon(repoRoot), canon(path)
	if filepath.Dir(cp) != filepath.Dir(cr) {
		return "", false
	}
	return trimID(filepath.Base(cp), s.stem(cr))
}

func (s sibling) Candidates(repoRoot string) []string {
	return readDirPaths(filepath.Dir(filepath.Clean(repoRoot)), func(name string) bool {
		_, ok := trimID(name, s.stem(repoRoot))
		return ok
	}, "")
}

type underRoot struct{ root, prefix string }

func (u underRoot) Path(repoRoot, id string) string {
	return filepath.Join(absRoot(repoRoot, u.root), u.prefix+id)
}

func (u underRoot) ID(repoRoot, path string) (string, bool) {
	cp := canon(path)
	if filepath.Dir(cp) != canon(absRoot(repoRoot, u.root)) {
		return "", false
	}
	return trimID(filepath.Base(cp), u.prefix)
}

func (u underRoot) Candidates(repoRoot string) []string {
	return readDirPaths(absRoot(repoRoot, u.root), func(name string) bool {
		_, ok := trimID(name, u.prefix)
		return ok
	}, "")
}

type nested struct{ root, leaf string }

func (n nested) Path(repoRoot, id string) string {
	return filepath.Join(absRoot(repoRoot, n.root), id, n.leaf)
}

func (n nested) ID(repoRoot, path string) (string, bool) {
	cp := canon(path)
	if filepath.Base(cp) != n.leaf {
		return "", false
	}
	dir := filepath.Dir(cp)
	if filepath.Dir(dir) != canon(absRoot(repoRoot, n.root)) {
		return "", false
	}
	return trimID(filepath.Base(dir), "")
}

func (n nested) Candidates(repoRoot string) []string {
	return readDirPaths(absRoot(repoRoot, n.root), func(name string) bool {
		return checkID(name) == nil
	}, n.leaf)
}

func absRoot(repoRoot, root string) string {
	if filepath.IsAbs(root) {
		return filepath.Clean(root)
	}
	return filepath.Join(repoRoot, root)
}

func trimID(name, prefix string) (string, bool) {
	if !strings.HasPrefix(name, prefix) {
		return "", false
	}
	id := strings.TrimPrefix(name, prefix)
	if checkID(id) != nil {
		return "", false
	}
	return id, true
}

// readDirPaths lists directories under dir whose names satisfy keep, joined
// with leaf when leaf is non-empty. Entries that are not directories, or whose
// leaf is missing, are skipped.
func readDirPaths(dir string, keep func(name string) bool, leaf string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !keep(e.Name()) {
			continue
		}
		p := filepath.Join(dir, e.Name(), leaf)
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

// checkID rejects ids that are unsafe to use as a single path element or as
// part of a branch name.
func checkID(id string) error {
	switch {
	case id == "":
		return fmt.Errorf("%w: empty", ErrInvalidID)
	case len(id) > 200:
		return fmt.Errorf("%w: longer than 200 bytes", ErrInvalidID)
	case id == "." || strings.Contains(id, ".."):
		return fmt.Errorf("%w: %q contains a dot segment", ErrInvalidID, id)
	case strings.HasPrefix(id, "-"):
		return fmt.Errorf("%w: %q starts with '-'", ErrInvalidID, id)
	}
	for _, r := range id {
		if r == '/' || r == '\\' || r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: %q contains a path separator or control character", ErrInvalidID, id)
		}
	}
	return nil
}

// canon resolves symlinks in p, including for the deepest existing ancestor
// when p itself does not exist yet. git reports resolved paths (on macOS
// /var/... appears as /private/var/...), so comparisons go through canon.
func canon(p string) string {
	p = filepath.Clean(p)
	var tail []string
	cur := p
	for {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				r = filepath.Join(r, tail[i])
			}
			return r
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}
