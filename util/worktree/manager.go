package worktree

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Spec describes a worktree to create.
type Spec struct {
	// ID is the caller's identifier; the Placement maps it to a path. It must
	// be path-safe (no separators, dot segments or control characters).
	ID string
	// Branch, when non-empty, creates and checks out a new branch with this
	// name (`git worktree add -b`). It takes precedence over the manager's
	// branch namer.
	Branch string
	// Detached forces a detached checkout even when a branch namer is
	// configured. Without Branch, a namer or Detached the worktree is detached.
	Detached bool
	// BaseRef overrides the manager's base-ref policy for this worktree. It
	// must resolve to a commit.
	BaseRef string
	// Path overrides the Placement for this worktree. Such a worktree is
	// invisible to [Manager.List] unless the Placement also maps it back.
	Path string
}

// Worktree is a git worktree registered against the manager's repository.
type Worktree struct {
	ID       string
	Path     string
	Branch   string // "" when detached
	HEAD     string
	RepoRoot string
	Locked   bool
	Prunable bool
	ModTime  time.Time // modification time of Path; zero when it is absent
}

// Status is a point-in-time safety report on one worktree.
type Status struct {
	// Dirty is true when the worktree has modified, staged or untracked
	// files. Ignored files are not counted.
	Dirty bool
	// UnreachableCommits counts commits reachable from HEAD but from no
	// branch, remote-tracking ref or tag: commits that removal would orphan.
	UnreachableCommits int
	// AheadOfBase counts commits on HEAD that the manager's base ref does not
	// contain; -1 when the base ref could not be resolved.
	AheadOfBase int
	// Branch is the checked-out branch, "" when detached.
	Branch string
	Locked bool
}

// BranchPolicy says what happens to the worktree's branch on removal.
type BranchPolicy int

const (
	// DeleteIfMerged deletes the branch with `git branch -d`, which git
	// refuses unless the branch is merged. It is the zero value.
	DeleteIfMerged BranchPolicy = iota
	// KeepBranch never touches the branch.
	KeepBranch
	// DeleteBranch deletes the branch unconditionally (`git branch -D`). It
	// requires [RemoveOptions.Force] or shipped-work proof from a sweep policy.
	DeleteBranch
)

// RemoveOptions tunes [Manager.Remove].
type RemoveOptions struct {
	// Force skips the dirty, unreachable-commit, ahead-of-base and
	// inspect-failure checks and passes --force to git. It never overrides a
	// lock.
	Force  bool
	Branch BranchPolicy
}

// RemoveResult reports what Remove did.
type RemoveResult struct {
	Removed bool
	// Reason is set when Removed is false: "absent", "dirty",
	// "unreachable-commits", "ahead-of-base", "locked" or
	// "inspect-failed: <cause>". For "absent" git's stale registration, if
	// any, was pruned.
	Reason string
	Status Status
	// BranchDeleted is true when the branch was deleted after removal.
	BranchDeleted bool
	// BranchNote explains why a branch was kept when the policy asked for
	// deletion (typically git refusing `branch -d` on an unmerged branch).
	BranchNote string
}

// Guard vetoes creating a worktree at wtPath by returning an error.
type Guard func(ctx context.Context, repoRoot, wtPath string) error

// Option configures a [Manager].
type Option func(*Manager)

// WithRunner replaces the default git [Runner].
func WithRunner(r Runner) Option {
	return func(m *Manager) {
		if r != nil {
			m.r = r
		}
	}
}

// WithPlacement replaces the default placement, Sibling("").
func WithPlacement(p Placement) Option {
	return func(m *Manager) {
		if p != nil {
			m.pl = p
		}
	}
}

// WithBaseRef replaces the default base policy, [DefaultBase].
func WithBaseRef(b BaseRef) Option {
	return func(m *Manager) {
		if b != nil {
			m.base = b
		}
	}
}

// WithFetch makes Create run a best-effort `git fetch origin` first when the
// repository has an origin remote. A failed fetch is ignored: the base policy
// falls back to a local ref.
func WithFetch() Option { return func(m *Manager) { m.fetch = true } }

// WithBranchNamer makes Create check out a new branch named namer(id) unless
// the [Spec] says otherwise.
func WithBranchNamer(namer func(id string) string) Option {
	return func(m *Manager) { m.namer = namer }
}

// WithGuard appends a creation guard.
func WithGuard(g Guard) Option {
	return func(m *Manager) {
		if g != nil {
			m.guards = append(m.guards, g)
		}
	}
}

// Manager creates, inspects and removes worktrees of one repository. It holds
// no per-worktree state; a mutex serializes mutations issued through the same
// Manager. Separate processes (or Managers) are serialized only by git's own
// locking.
type Manager struct {
	repoRoot string
	r        Runner
	pl       Placement
	base     BaseRef
	fetch    bool
	namer    func(string) string
	guards   []Guard
	mu       sync.Mutex
}

// New returns a Manager for the repository whose top-level directory is
// repoRoot. The path is validated with git and never inferred from the process
// working directory; use [FindRepoRoot] to locate it from a subdirectory. The
// stored root has symlinks resolved.
func New(repoRoot string, opts ...Option) (*Manager, error) {
	m := &Manager{r: ExecRunner(), pl: Sibling(""), base: DefaultBase()}
	for _, o := range opts {
		o(m)
	}
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, err
	}
	out, err := m.r.Run(context.Background(), abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrNotRepo, abs, err)
	}
	top := canon(trimNL(out))
	if top != canon(abs) {
		return nil, fmt.Errorf("%w: %s is inside the working tree at %s", ErrNotRepo, abs, top)
	}
	m.repoRoot = top
	return m, nil
}

// RepoRoot returns the resolved repository root the Manager is bound to.
func (m *Manager) RepoRoot() string { return m.repoRoot }

// FindRepoRoot walks up from start looking for a .git directory or file (the
// file form is a linked worktree) and returns the absolute directory that
// contains it.
func FindRepoRoot(start string) (string, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	cur := abs
	for {
		if _, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
			return cur, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("%w: no .git found at or above %s", ErrNotRepo, start)
		}
		cur = parent
	}
}

// Create adds a worktree. It refuses a path that already exists
// ([ErrPathExists]) and an unsafe id ([ErrInvalidID]).
func (m *Manager) Create(ctx context.Context, spec Spec) (Worktree, error) {
	if err := checkID(spec.ID); err != nil {
		return Worktree{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	path := spec.Path
	if path == "" {
		path = m.pl.Path(m.repoRoot, spec.ID)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return Worktree{}, err
	}
	for _, g := range m.guards {
		if err = g(ctx, m.repoRoot, path); err != nil {
			return Worktree{}, err
		}
	}

	// Collision guard: a pre-existing path means a prior run leaked it or two
	// runs share an id; say how to clean up instead of surfacing raw git text.
	if _, err = os.Lstat(path); err == nil {
		return Worktree{}, fmt.Errorf("%w: %s — remove the stale worktree "+
			"(`git -C %s worktree remove --force %s` then `git -C %s worktree prune`) or pick a fresh id",
			ErrPathExists, path, m.repoRoot, path, m.repoRoot)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Worktree{}, fmt.Errorf("stat worktree path %s: %w", path, err)
	}

	branch := spec.Branch
	if branch == "" && !spec.Detached && m.namer != nil {
		branch = m.namer(spec.ID)
	}
	if branch != "" {
		if strings.HasPrefix(branch, "-") {
			return Worktree{}, fmt.Errorf("invalid branch name %q", branch)
		}
		if _, err = m.r.Run(ctx, m.repoRoot, "check-ref-format", "--branch", branch); err != nil {
			return Worktree{}, fmt.Errorf("invalid branch name %q: %w", branch, err)
		}
	}

	if m.fetch && m.hasOrigin(ctx) {
		_, _ = m.r.Run(ctx, m.repoRoot, "fetch", "origin") // best-effort; the base policy falls back locally
	}
	base := spec.BaseRef
	if base != "" {
		if _, err = resolveCommit(ctx, m.r, m.repoRoot, base); err != nil {
			return Worktree{}, fmt.Errorf("base ref %q does not resolve: %w", base, err)
		}
	} else if base, err = m.base(ctx, m.r, m.repoRoot); err != nil {
		return Worktree{}, err
	}

	if err = os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return Worktree{}, fmt.Errorf("create worktree parent %s: %w", filepath.Dir(path), err)
	}
	args := []string{"worktree", "add"}
	if branch != "" {
		args = append(args, "-b", branch, path, base)
	} else {
		args = append(args, "--detach", path, base)
	}
	if _, err = m.r.Run(ctx, m.repoRoot, args...); err != nil {
		hint := fmt.Sprintf(" (path may already be registered — prune stale worktrees with `git -C %s worktree prune`)", m.repoRoot)
		if branch != "" {
			hint = fmt.Sprintf(" (branch %q may already exist — prune stale worktrees with `git -C %s worktree prune`)", branch, m.repoRoot)
		}
		return Worktree{}, fmt.Errorf("create git worktree %s from %s%s: %w", path, base, hint, err)
	}
	head, err := m.r.Run(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return Worktree{}, fmt.Errorf("read HEAD of new worktree %s: %w", path, err)
	}
	return Worktree{
		ID: spec.ID, Path: path, Branch: branch, HEAD: trimNL(head),
		RepoRoot: m.repoRoot, ModTime: modTime(path),
	}, nil
}

func (m *Manager) hasOrigin(ctx context.Context) bool {
	out, err := m.r.Run(ctx, m.repoRoot, "remote")
	return err == nil && slices.Contains(strings.Fields(out), "origin")
}

// List returns the worktrees git reports that belong to the Placement,
// ordered by id. The main working tree and worktrees created elsewhere are
// not included. Worktree.Path is spelled as the Placement spells it.
func (m *Manager) List(ctx context.Context) ([]Worktree, error) {
	entries, err := m.listRaw(ctx)
	if err != nil {
		return nil, err
	}
	var out []Worktree
	for _, e := range entries {
		if e.Bare || canon(e.Path) == m.repoRoot {
			continue
		}
		id, ok := m.pl.ID(m.repoRoot, e.Path)
		if !ok {
			continue
		}
		path := m.pl.Path(m.repoRoot, id)
		out = append(out, Worktree{
			ID: id, Path: path, Branch: e.Branch, HEAD: e.HEAD, RepoRoot: m.repoRoot,
			Locked: e.Locked, Prunable: e.Prunable, ModTime: modTime(path),
		})
	}
	slices.SortFunc(out, func(a, b Worktree) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

func (m *Manager) listRaw(ctx context.Context) ([]rawEntry, error) {
	out, err := m.r.Run(ctx, m.repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("list worktrees: %w", err)
	}
	return parsePorcelain(out), nil
}

func modTime(path string) time.Time {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// Inspect reports whether removing wt would lose work. Any git failure is
// returned as an error; callers must treat an error as "keep".
func (m *Manager) Inspect(ctx context.Context, wt Worktree) (Status, error) {
	if _, err := os.Stat(wt.Path); err != nil {
		return Status{}, fmt.Errorf("inspect %s: %w", wt.Path, err)
	}
	var st Status
	out, err := m.r.Run(ctx, wt.Path, "status", "--porcelain")
	if err != nil {
		return Status{}, fmt.Errorf("inspect %s: status: %w", wt.Path, err)
	}
	st.Dirty = strings.TrimSpace(out) != ""

	// Not `--not --all`: --all includes this worktree's own HEAD, which
	// would always report 0.
	out, err = m.r.Run(ctx, wt.Path, "rev-list", "--count", "HEAD", "--not", "--branches", "--remotes", "--tags")
	if err != nil {
		return Status{}, fmt.Errorf("inspect %s: unreachable commits: %w", wt.Path, err)
	}
	if st.UnreachableCommits, err = strconv.Atoi(trimNL(out)); err != nil {
		return Status{}, fmt.Errorf("inspect %s: unreachable commits: %w", wt.Path, err)
	}

	out, err = m.r.Run(ctx, wt.Path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return Status{}, fmt.Errorf("inspect %s: branch: %w", wt.Path, err)
	}
	if b := trimNL(out); b != "HEAD" {
		st.Branch = b
	}

	st.AheadOfBase = m.aheadOfBase(ctx, wt.Path)

	entries, err := m.listRaw(ctx)
	if err != nil {
		return Status{}, err
	}
	cp := canon(wt.Path)
	for _, e := range entries {
		if canon(e.Path) == cp {
			st.Locked = e.Locked
			break
		}
	}
	return st, nil
}

func (m *Manager) aheadOfBase(ctx context.Context, wtPath string) int {
	base, err := m.base(ctx, m.r, m.repoRoot)
	if err != nil {
		return -1
	}
	oid, err := resolveCommit(ctx, m.r, m.repoRoot, base)
	if err != nil {
		return -1
	}
	out, err := m.r.Run(ctx, wtPath, "rev-list", "--count", oid+"..HEAD")
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(trimNL(out))
	if err != nil {
		return -1
	}
	return n
}

// Remove removes wt if doing so loses no work. See the package documentation
// for what is preserved. A preserved worktree is not an error: Removed is
// false and Reason says why. Errors are reserved for git failures during the
// removal itself and for invalid options.
func (m *Manager) Remove(ctx context.Context, wt Worktree, opts RemoveOptions) (RemoveResult, error) {
	if opts.Branch == DeleteBranch && !opts.Force {
		return RemoveResult{}, ErrForceRequired
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.remove(ctx, wt, opts, false)
}

// remove is Remove without locking. shipped means a sweep policy proved the
// worktree's commits are on the remote: ahead/unreachable checks are waived
// and the branch may be force-deleted, but dirty and locked still block.
func (m *Manager) remove(ctx context.Context, wt Worktree, opts RemoveOptions, shipped bool) (RemoveResult, error) {
	if wt.Path == "" {
		return RemoveResult{}, errors.New("worktree has no path")
	}
	if _, err := os.Lstat(wt.Path); errors.Is(err, fs.ErrNotExist) {
		_, _ = m.r.Run(ctx, m.repoRoot, "worktree", "prune") // drop a stale registration, if any
		return RemoveResult{Reason: "absent"}, nil
	}
	st, ierr := m.Inspect(ctx, wt)
	if ierr != nil && !opts.Force {
		return RemoveResult{Reason: "inspect-failed: " + ierr.Error()}, nil
	}
	res := RemoveResult{Status: st}
	if ierr != nil {
		res.Status.Branch = wt.Branch
	}
	if st.Locked {
		res.Reason = "locked"
		return res, nil
	}
	if !opts.Force {
		switch {
		case st.Dirty:
			res.Reason = "dirty"
		case !shipped && st.UnreachableCommits > 0:
			res.Reason = "unreachable-commits"
		case !shipped && st.AheadOfBase > 0:
			res.Reason = "ahead-of-base"
		}
		if res.Reason != "" {
			return res, nil
		}
	}

	args := []string{"worktree", "remove"}
	if opts.Force {
		args = append(args, "--force") // a single --force: locked worktrees were refused above
	}
	if _, err := m.r.Run(ctx, m.repoRoot, append(args, wt.Path)...); err != nil {
		if _, statErr := os.Lstat(wt.Path); !errors.Is(statErr, fs.ErrNotExist) {
			return res, fmt.Errorf("remove git worktree %s: %w", wt.Path, err)
		}
		// The directory is gone but git still lists it; clear the registry.
		_, _ = m.r.Run(ctx, m.repoRoot, "worktree", "prune")
	}
	res.Removed = true

	if branch := res.Status.Branch; branch != "" && opts.Branch != KeepBranch {
		flag := "-d"
		if opts.Branch == DeleteBranch {
			flag = "-D"
		}
		if _, err := m.r.Run(ctx, m.repoRoot, "branch", flag, "--", branch); err != nil {
			res.BranchNote = err.Error()
		} else {
			res.BranchDeleted = true
		}
	}
	return res, nil
}
