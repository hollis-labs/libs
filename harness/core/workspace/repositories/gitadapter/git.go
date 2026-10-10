// Package gitadapter implements bounded local Git operations from explicit
// host inputs. It uses no worktree manager because parent creation and broad
// pruning must remain unavailable. Confinement assumes cooperating mutation
// locks and private base custody; arbitrary same-user swaps remain outside it.
package gitadapter

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"github.com/hollis-labs/substrate/harness/workspace/repositories"
)

var errGit = errors.New("repository Git operation refused")

const maxOutput = 4 << 20

// Port uses an explicit absolute executable; it never searches PATH or homes.
type Port struct {
	executable string
	platform   string
	trace      func([]string)
	cow        cloner
}

func New(executable string) *Port {
	return &Port{executable: executable, platform: runtime.GOOS, cow: newCloner()}
}
func (p *Port) Supported(r repositories.Request) bool {
	return p != nil && (p.platform == "linux" || p.platform == "darwin") && filepath.IsAbs(p.executable) && (r.Mode == repositories.Worktree || r.Mode == repositories.Readonly || r.Mode == repositories.Checkout && r.Existing || r.Mode == repositories.Clone && !r.Existing && p.cow != nil)
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(v []byte) (int, error) {
	if b.Len()+len(v) > maxOutput {
		return 0, errGit
	}
	return b.Buffer.Write(v)
}
func (p *Port) run(ctx context.Context, dir string, args ...string) (string, int, error) {
	if p == nil || !filepath.IsAbs(p.executable) || !filepath.IsAbs(dir) {
		return "", -1, errGit
	}
	size := 0
	for _, a := range args {
		size += len(a)
		if len(a) > 4096 || strings.ContainsRune(a, 0) {
			return "", -1, errGit
		}
	}
	if size > 65536 {
		return "", -1, errGit
	}
	if p.trace != nil {
		p.trace(append([]string(nil), args...))
	}
	child, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	prefix := []string{"--no-pager", "--no-optional-locks", "-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "submodule.recurse=false", "-c", "protocol.allow=never", "-c", "protocol.file.allow=never"}
	cmd := exec.CommandContext(child, p.executable, append(prefix, args...)...)
	cmd.Dir = dir
	cmd.WaitDelay = time.Second
	cmd.Env = []string{"LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=" + os.DevNull, "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1", "GIT_ATTR_NOSYSTEM=1", "GIT_LFS_SKIP_SMUDGE=1", "HOME=" + os.DevNull, "PATH=" + filepath.Dir(p.executable)}
	var out, stderr limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	e := cmd.Run()
	if e != nil {
		code := -1
		var exit *exec.ExitError
		if errors.As(e, &exit) {
			code = exit.ExitCode()
		}
		return "", code, errGit
	}
	return out.String(), 0, nil
}
func canonical(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errGit
	}
	actual, e := filepath.EvalSymlinks(path)
	if e != nil || actual != path {
		return "", errGit
	}
	return actual, nil
}

// Identity observes one explicit, canonical owned directory for host capture.
func Identity(path string) (string, error) {
	if _, e := canonical(path); e != nil {
		return "", e
	}
	st, e := os.Lstat(path)
	if e != nil || !safeDirectory(st) || identity(st) == "" {
		return "", errGit
	}
	return identity(st), nil
}

type pinned struct {
	root *os.Root
	path string
	info fs.FileInfo
}

func pin(path string) (p pinned, e error) {
	if _, e = canonical(path); e != nil {
		return p, e
	}
	st, e := os.Lstat(path)
	if e != nil || !safeDirectory(st) {
		return p, errGit
	}
	root, e := os.OpenRoot(path)
	if e != nil {
		return p, errGit
	}
	actual, e := root.Stat(".")
	if e != nil || !os.SameFile(st, actual) {
		root.Close()
		return p, errGit
	}
	return pinned{root: root, path: path, info: st}, nil
}
func (p pinned) valid() bool {
	st, e := os.Lstat(p.path)
	return e == nil && safeDirectory(st) && os.SameFile(p.info, st)
}

type roots struct{ s, m, b pinned }

func (q *roots) close()      { q.s.root.Close(); q.m.root.Close(); q.b.root.Close() }
func (q *roots) valid() bool { return q.s.valid() && q.m.valid() && q.b.valid() }
func openRoots(r repositories.Request) (q *roots, e error) {
	q = &roots{}
	q.s, e = pin(r.Source.Path)
	if e != nil {
		return nil, e
	}
	q.m, e = pin(r.Common.Path)
	if e != nil {
		q.s.root.Close()
		return nil, e
	}
	q.b, e = pin(r.Base.Path)
	if e != nil {
		q.s.root.Close()
		q.m.root.Close()
		return nil, e
	}
	if !r.Existing && (q.m.info.Mode().Perm()&0300 != 0300 || q.b.info.Mode().Perm()&0300 != 0300) {
		q.close()
		return nil, errGit
	}
	if identity(q.s.info) != r.SourceIdentity || identity(q.m.info) != r.CommonIdentity || filepath.Dir(r.Path) != r.Base.Path {
		q.close()
		return nil, errGit
	}
	return q, nil
}
func trim(s string) string { return strings.TrimSuffix(s, "\n") }
func oid(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, v := range s {
		if !(v >= '0' && v <= '9' || v >= 'a' && v <= 'f') {
			return false
		}
	}
	return true
}
func (p *Port) repository(ctx context.Context, r repositories.Request, path string) (repositories.Observation, error) {
	return p.repositoryAt(ctx, r, path, r.Common.Path)
}
func (p *Port) repositoryAt(ctx context.Context, r repositories.Request, path, commonPath string) (repositories.Observation, error) {
	top, _, e := p.run(ctx, path, "rev-parse", "--show-toplevel")
	if e != nil || trim(top) != path {
		return repositories.Observation{}, errGit
	}
	common, _, e := p.run(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if e != nil || trim(common) != commonPath {
		return repositories.Observation{}, errGit
	}
	if _, e = canonical(trim(common)); e != nil {
		return repositories.Observation{}, errGit
	}
	head, _, e := p.run(ctx, path, "rev-parse", "--verify", "HEAD^{commit}")
	if e != nil || !oid(trim(head)) {
		return repositories.Observation{}, errGit
	}
	branch, code, e := p.run(ctx, path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if e != nil && code != 1 {
		return repositories.Observation{}, errGit
	}
	if code == 1 {
		branch = ""
	}
	if p.refuseCode(ctx, path, commonPath) != nil {
		return repositories.Observation{}, errGit
	}
	status, _, e := p.run(ctx, path, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if e != nil {
		return repositories.Observation{}, errGit
	}
	return repositories.Observation{Exists: true, Path: path, CommonPath: commonPath, RepositoryID: r.RepositoryID, SourceIdentity: r.SourceIdentity, CommonIdentity: r.CommonIdentity, Branch: trim(branch), Head: trim(head), Dirty: status != ""}, nil
}
func (p *Port) Source(ctx context.Context, r repositories.Request) (repositories.Observation, error) {
	q, e := openRoots(r)
	if e != nil {
		return repositories.Observation{}, e
	}
	defer q.close()
	if p.refuseCheckoutCode(ctx, r) != nil {
		return repositories.Observation{}, errGit
	}
	o, e := p.repository(ctx, r, r.Source.Path)
	if e != nil || !q.valid() {
		return repositories.Observation{}, errGit
	}
	return o, nil
}
func (p *Port) ResolveBase(ctx context.Context, r repositories.Request) (string, error) {
	if !oid(r.BaseCommit) {
		return "", errGit
	}
	q, e := openRoots(r)
	if e != nil {
		return "", e
	}
	defer q.close()
	v, _, e := p.run(ctx, r.Source.Path, "rev-parse", "--verify", "--quiet", r.BaseCommit+"^{commit}")
	if e != nil || trim(v) != r.BaseCommit || !q.valid() {
		return "", errGit
	}
	return trim(v), nil
}
func (p *Port) BranchAvailable(ctx context.Context, r repositories.Request) error {
	if !repositories.ValidBranch(r.Branch) {
		return errGit
	}
	q, e := openRoots(r)
	if e != nil {
		return e
	}
	defer q.close()
	if _, _, e = p.run(ctx, r.Source.Path, "check-ref-format", "--branch", r.Branch); e != nil {
		return errGit
	}
	_, code, e := p.run(ctx, r.Source.Path, "show-ref", "--verify", "--quiet", "refs/heads/"+r.Branch)
	if code != 1 || e == nil || !q.valid() {
		return errGit
	}
	return nil
}

type registration struct {
	path, branch, head string
	locked, unknown    bool
}

func (p *Port) registration(ctx context.Context, r repositories.Request) (registration, bool, error) {
	raw, _, e := p.run(ctx, r.Source.Path, "worktree", "list", "--porcelain", "-z")
	if e != nil {
		return registration{}, false, errGit
	}
	var current registration
	have := false
	for _, field := range strings.Split(raw, "\x00") {
		if field == "" {
			if have && current.path == r.Path {
				return current, true, nil
			}
			current = registration{}
			have = false
			continue
		}
		switch {
		case strings.HasPrefix(field, "worktree "):
			current.path = strings.TrimPrefix(field, "worktree ")
			have = true
		case strings.HasPrefix(field, "HEAD "):
			current.head = strings.TrimPrefix(field, "HEAD ")
		case strings.HasPrefix(field, "branch refs/heads/"):
			current.branch = strings.TrimPrefix(field, "branch refs/heads/")
		case field == "detached":
			current.branch = ""
		case field == "locked" || strings.HasPrefix(field, "locked "):
			current.locked = true
		default:
			current.unknown = true
		}
	}
	return registration{}, false, nil
}
func (p *Port) Observe(ctx context.Context, r repositories.Request) (repositories.Observation, error) {
	if r.Mode == repositories.Clone {
		return p.observeClone(ctx, r)
	}
	q, e := openRoots(r)
	if e != nil {
		return repositories.Observation{}, e
	}
	defer q.close()
	st, e := q.b.root.Lstat(filepath.Base(r.Path))
	if os.IsNotExist(e) {
		if !q.valid() {
			return repositories.Observation{}, errGit
		}
		return repositories.Observation{Path: r.Path}, nil
	}
	if e != nil || !safeDirectory(st) {
		return repositories.Observation{}, errGit
	}
	if _, e = canonical(r.Path); e != nil {
		return repositories.Observation{}, errGit
	}
	o, e := p.repository(ctx, r, r.Path)
	if e != nil {
		return repositories.Observation{}, e
	}
	reg, exists, e := p.registration(ctx, r)
	if e != nil || !exists || reg.unknown || reg.head != o.Head || reg.branch != o.Branch || !q.valid() {
		return repositories.Observation{}, errGit
	}
	o.Locked = reg.locked
	return o, nil
}

// refuseCheckoutCode excludes external filter execution and partial-object
// fetching. Global/system config and hooks are disabled independently.
func (p *Port) refuseCheckoutCode(ctx context.Context, r repositories.Request, paths ...string) error {
	dir := r.Source.Path
	if len(paths) > 0 {
		dir = paths[0]
	}
	return p.refuseCode(ctx, dir, r.Common.Path)
}
func (p *Port) refuseCode(ctx context.Context, dir, commonPath string) error {
	scopes := []string{"--local"}
	configured, code, err := p.run(ctx, dir, "config", "--local", "--no-includes", "--bool", "--get", "extensions.worktreeConfig")
	if err != nil && code != 1 {
		return errGit
	}
	if code == 0 && trim(configured) == "true" {
		scopes = append(scopes, "--worktree")
	}
	for _, scope := range scopes {
		_, code, e := p.run(ctx, dir, "config", scope, "--no-includes", "--name-only", "--get-regexp", `^(filter\.|include\.|includeif\.|extensions\.partialclone|remote\..*\.promisor)`)
		if code != 1 || e == nil {
			return errGit
		}
	}
	for _, rel := range []string{"objects", "objects/info"} {
		actual, e := filepath.EvalSymlinks(filepath.Join(commonPath, rel))
		if e != nil || actual != filepath.Join(commonPath, rel) {
			return errGit
		}
	}
	if _, e := os.Lstat(filepath.Join(commonPath, "objects/info/alternates")); !os.IsNotExist(e) {
		return errGit
	}
	return nil
}
func (p *Port) Create(ctx context.Context, r repositories.Request, validate func(context.Context) error) (repositories.Observation, bool, error) {
	if r.Mode == repositories.Clone {
		return p.createClone(ctx, r, validate)
	}
	if !p.Supported(r) || r.Mode != repositories.Worktree || r.Existing || r.Ownership != repositories.Owned || !r.Base.PrivateCustody || validate == nil {
		return repositories.Observation{}, false, errGit
	}
	q, e := openRoots(r)
	if e != nil {
		return repositories.Observation{}, false, e
	}
	defer q.close()
	if o, e := p.Source(ctx, r); e != nil || !o.Exists {
		return repositories.Observation{}, false, errGit
	}
	if p.refuseCheckoutCode(ctx, r) != nil || p.BranchAvailable(ctx, r) != nil {
		return repositories.Observation{}, false, errGit
	}
	if _, e = p.ResolveBase(ctx, r); e != nil {
		return repositories.Observation{}, false, errGit
	}
	if o, e := p.Observe(ctx, r); e != nil || o.Exists {
		return repositories.Observation{}, false, errGit
	}
	if ctx.Err() != nil || validate(ctx) != nil || ctx.Err() != nil || !q.valid() {
		return repositories.Observation{}, false, errGit
	}
	// Recheck after the authority callback; no parent-creation API is available.
	if o, e := p.Observe(ctx, r); e != nil || o.Exists || p.BranchAvailable(ctx, r) != nil {
		return repositories.Observation{}, false, errGit
	}
	if !q.valid() || p.refuseCheckoutCode(ctx, r) != nil {
		return repositories.Observation{}, false, errGit
	}
	// Only the authorized leaf is created, exclusively beneath its existing
	// pinned parent. No parent creation or permission repair is performed.
	if e = q.b.root.Mkdir(filepath.Base(r.Path), 0700); e != nil {
		return repositories.Observation{}, true, errGit
	}
	if !q.valid() {
		return repositories.Observation{}, true, errGit
	}
	if _, _, e = p.run(ctx, r.Source.Path, "worktree", "add", "-b", r.Branch, "--", r.Path, r.BaseCommit); e != nil {
		return repositories.Observation{}, true, errGit
	}
	o, e := p.Observe(ctx, r)
	if e != nil || !q.valid() {
		return o, true, errGit
	}
	return o, true, nil
}
func (p *Port) Safety(ctx context.Context, r repositories.Request, a effects.AttachmentEvidence) (repositories.Safety, error) {
	q, e := openRoots(r)
	if e != nil {
		return repositories.Safety{}, e
	}
	defer q.close()
	if p.refuseCheckoutCode(ctx, r) != nil {
		return repositories.Safety{}, errGit
	}
	o, e := p.Observe(ctx, r)
	if e != nil || !o.Exists || o.Head != a.Head || o.Branch != a.Branch {
		return repositories.Safety{}, errGit
	}
	s := repositories.Safety{Head: o.Head, Locked: o.Locked}
	raw, _, e := p.run(ctx, r.Path, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching", "--ignore-submodules=none")
	if e != nil {
		return s, errGit
	}
	records := strings.Split(raw, "\x00")
	for i := 0; i < len(records)-1; i++ {
		line := records[i]
		if len(line) < 4 || line[2] != ' ' || line[3:] == "" {
			s.Unknown = true
			continue
		}
		xy := line[:2]
		switch xy {
		case "??":
			s.Untracked = true
		case "!!":
			s.Ignored = true
		default:
			s.Dirty = true
			for _, c := range xy {
				if !strings.ContainsRune(" MADRCUT", c) {
					s.Unknown = true
				}
			}
			if strings.ContainsAny(xy, "RC") {
				i++
				if i >= len(records)-1 || records[i] == "" {
					s.Unknown = true
				}
			}
		}
	}
	if raw != "" && !strings.HasSuffix(raw, "\x00") {
		s.Unknown = true
	}
	// Status may hide modified bytes behind assume-unchanged or skip-worktree
	// flags. Observe the index flags without clearing them; any nonordinary or
	// malformed entry leaves removal safety unknown even if status is clean.
	flags, _, e := p.run(ctx, r.Path, "ls-files", "-v", "-z")
	if e != nil {
		s.Unknown = true
		return s, errGit
	}
	if flags != "" && !strings.HasSuffix(flags, "\x00") {
		s.Unknown = true
	}
	flagRecords := strings.Split(flags, "\x00")
	for n, line := range flagRecords {
		if line == "" {
			if n < len(flagRecords)-1 {
				s.Unknown = true
			}
			continue
		}
		if len(line) < 3 || line[0] != 'H' || line[1] != ' ' {
			s.Unknown = true
		}
	}
	stage, _, e := p.run(ctx, r.Path, "ls-files", "--stage", "-z")
	if e != nil {
		return s, errGit
	}
	for _, line := range strings.Split(stage, "\x00") {
		if strings.HasPrefix(line, "160000 ") {
			s.Unknown = true
		}
	}
	if !oid(a.Head) {
		return s, errGit
	}
	raw, _, e = p.run(ctx, r.Path, "rev-list", "--count", a.Head+"..HEAD")
	if e != nil {
		return s, errGit
	}
	s.Ahead, e = strconv.Atoi(trim(raw))
	if e != nil || s.Ahead < 0 {
		return s, errGit
	}
	gitdir, _, e := p.run(ctx, r.Path, "rev-parse", "--path-format=absolute", "--git-dir")
	if e != nil {
		return s, errGit
	}
	admin := trim(gitdir)
	expected := filepath.Join(r.Common.Path, "worktrees")
	rel, e := filepath.Rel(expected, admin)
	if e != nil || rel == "." || strings.Contains(rel, string(filepath.Separator)) || rel == ".." {
		s.Unknown = true
		return s, nil
	}
	if _, e = canonical(admin); e != nil {
		return s, errGit
	}
	entries, e := os.ReadDir(admin)
	if e != nil {
		return s, errGit
	}
	for _, entry := range entries {
		name := entry.Name()
		switch name {
		case "HEAD", "index", "commondir", "gitdir", "ORIG_HEAD", "config.worktree":
			if !entry.Type().IsRegular() {
				s.Unknown = true
			}
		case "refs":
			// Git may create an empty per-worktree ref namespace. Any populated
			// namespace could contain unshipped work and is retained as unknown.
			if !entry.IsDir() {
				s.Unknown = true
				continue
			}
			children, e := os.ReadDir(filepath.Join(admin, name))
			if e != nil {
				return s, errGit
			}
			if len(children) != 0 {
				s.Unknown = true
			}

		case "logs":
			if !entry.IsDir() {
				s.Unknown = true
				continue
			}
			logs, e := os.ReadDir(filepath.Join(admin, name))
			if e != nil {
				return s, errGit
			}
			for _, log := range logs {
				if log.Name() != "HEAD" || !log.Type().IsRegular() {
					s.Unknown = true
				}
			}
		default:
			s.Unknown = true
		}
		if strings.HasSuffix(name, ".lock") || name == "locked" {
			s.Locked = true
		}
	}
	s.Unreachable, e = p.privateHistoryCount(ctx, r, q.m.root, admin, o.Head)
	if e != nil {
		s.Unknown = true
		return s, errGit
	}
	seen := 0
	e = filepath.WalkDir(r.Common.Path, func(path string, d fs.DirEntry, e error) error {
		if e != nil || ctx.Err() != nil {
			return errGit
		}
		seen++
		if seen > 20000 {
			return errGit
		}
		if d.Type()&os.ModeSymlink != 0 {
			s.Unknown = true
		}
		if strings.HasSuffix(d.Name(), ".lock") {
			s.Locked = true
		}
		return nil
	})
	if e != nil || !q.valid() {
		return s, errGit
	}
	s.Complete = true
	return s, nil
}
func (p *Port) Remove(ctx context.Context, r repositories.Request, a effects.AttachmentEvidence, validate func(context.Context) error) (bool, error) {
	if !p.Supported(r) || r.Mode != repositories.Worktree || r.Existing || r.Ownership != repositories.Owned || !r.Base.PrivateCustody || !a.Created || a.Mode != string(r.Mode) || a.Ownership != string(r.Ownership) || a.Path != r.Path || a.SourcePath != r.Source.Path || a.CommonPath != r.Common.Path || a.CommonIdentity != r.CommonIdentity || a.SourceIdentity != r.SourceIdentity || a.RepositoryID != r.RepositoryID || a.Branch != r.Branch || a.BaseCommit != r.BaseCommit || validate == nil {
		return false, errGit
	}
	q, e := openRoots(r)
	if e != nil {
		return false, e
	}
	defer q.close()
	if ctx.Err() != nil || validate(ctx) != nil || ctx.Err() != nil || !q.valid() {
		return false, errGit
	}
	s, e := p.Safety(ctx, r, a)
	if e != nil || !s.Complete || s.Head != a.Head || s.Dirty || s.Untracked || s.Ignored || s.Unknown || s.Locked || s.Unreachable != 0 || s.Ahead != 0 || !q.valid() {
		return false, errGit
	}
	if ctx.Err() != nil || validate(ctx) != nil || ctx.Err() != nil || !q.valid() {
		return false, errGit
	}
	// Authority refresh is a host callback and can change the observed resource.
	// Recheck safety after that final callback, immediately before Git removal.
	s, e = p.Safety(ctx, r, a)
	if e != nil || !s.Complete || s.Head != a.Head || s.Dirty || s.Untracked || s.Ignored || s.Unknown || s.Locked || s.Unreachable != 0 || s.Ahead != 0 || ctx.Err() != nil || !q.valid() {
		return false, errGit
	}
	if _, _, e = p.run(ctx, r.Source.Path, "worktree", "remove", "--", r.Path); e != nil {
		return true, errGit
	}
	o, e := p.Observe(ctx, r)
	if e != nil || o.Exists || !q.valid() {
		return true, errGit
	}
	return true, nil
}
