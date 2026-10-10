package gitadapter

import (
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/hollis-labs/substrate/harness/workspace/repositories"
)

// Clone attachments are independent private repositories: a fresh `git init`
// with an empty template, object files cloned copy on write from the source's
// object store, a new branch at the full pinned base and tracked files cloned
// from a source whose HEAD is that base with no tracked change. No .git file,
// config, index, ref, hook or alternate is shared or copied, and no file is
// ever written by plain copy in place of a clone.

const (
	maxCloneObjects = 1 << 16
	maxCloneFiles   = 1 << 17
	maxCheckoutArgs = 32 << 10
)

type cloneFile struct {
	path string
	exec bool
}
type clonePlan struct {
	format  string
	objects []string
	files   []cloneFile
	links   []string
}

func hexName(s string, n ...int) bool {
	ok := false
	for _, l := range n {
		ok = ok || len(s) == l
	}
	for _, v := range s {
		if !(v >= '0' && v <= '9' || v >= 'a' && v <= 'f') {
			return false
		}
	}
	return ok
}

// treePath accepts only plain relative tree paths with no Git metadata.
func treePath(p string) bool {
	if p == "" || !utf8.ValidString(p) || strings.ContainsRune(p, 0) || strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

// objectFiles lists immutable object files: loose objects and complete
// pack/idx(/rev) sets. Derived indexes, keep markers and temporary files are
// not cloned; promisor packs and alternates are refused.
func objectFiles(root *os.Root, format string) ([]string, string) {
	loose := 38
	if format == "sha256" {
		loose = 62
	}
	top, e := fs.ReadDir(root.FS(), "objects")
	if e != nil {
		return nil, "object_store_unreadable"
	}
	var out []string
	packs := map[string]int{}
	for _, d := range top {
		name := d.Name()
		switch {
		case hexName(name, 2) && d.IsDir():
			entries, e := fs.ReadDir(root.FS(), "objects/"+name)
			if e != nil {
				return nil, "object_store_unreadable"
			}
			for _, o := range entries {
				if !o.Type().IsRegular() || !hexName(o.Name(), loose) {
					if strings.HasPrefix(o.Name(), "tmp_") {
						continue
					}
					return nil, "object_store_unrecognized"
				}
				out = append(out, name+"/"+o.Name())
			}
		case name == "pack" && d.IsDir():
			entries, e := fs.ReadDir(root.FS(), "objects/pack")
			if e != nil {
				return nil, "object_store_unreadable"
			}
			for _, o := range entries {
				n := o.Name()
				stem, ext, _ := strings.Cut(n, ".")
				if !strings.HasPrefix(stem, "pack-") || !hexName(strings.TrimPrefix(stem, "pack-"), 40, 64) {
					if strings.HasPrefix(n, "tmp_") || n == "multi-pack-index" || strings.HasPrefix(n, "multi-pack-index") {
						continue
					}
					return nil, "object_store_unrecognized"
				}
				if !o.Type().IsRegular() {
					return nil, "object_store_unrecognized"
				}
				switch ext {
				case "pack":
					packs[stem] |= 1
					out = append(out, "pack/"+n)
				case "idx":
					packs[stem] |= 2
					out = append(out, "pack/"+n)
				case "rev":
					out = append(out, "pack/"+n)
				case "promisor":
					return nil, "partial_clone_unsupported"
				case "keep", "bitmap", "mtimes":
				default:
					return nil, "object_store_unrecognized"
				}
			}
		case name == "info" && d.IsDir():
		default:
			return nil, "object_store_unrecognized"
		}
		if len(out) > maxCloneObjects {
			return nil, "object_store_too_large"
		}
	}
	for _, v := range packs {
		if v != 3 {
			return nil, "object_store_incomplete"
		}
	}
	if len(out) == 0 {
		return nil, "object_store_empty"
	}
	return out, ""
}

// planClone observes only. A nonempty reason is a stable answer that copy on
// write cannot construct this attachment honestly; an error is unknown.
func (p *Port) planClone(ctx context.Context, r repositories.Request, q *roots) (clonePlan, string, error) {
	var plan clonePlan
	format, _, e := p.run(ctx, r.Source.Path, "rev-parse", "--show-object-format")
	if e != nil {
		return plan, "", errGit
	}
	plan.format = trim(format)
	if plan.format != "sha1" && plan.format != "sha256" || len(r.BaseCommit) != map[string]int{"sha1": 40, "sha256": 64}[plan.format] {
		return plan, "object_format_mismatch", nil
	}
	source, e := p.repository(ctx, r, r.Source.Path)
	if e != nil {
		return plan, "", errGit
	}
	// A clone never manufactures a clean base from a live, moved or dirty
	// source: the source HEAD is the base and no tracked change is present.
	if source.Head != r.BaseCommit {
		return plan, "source_not_at_base", nil
	}
	status, _, e := p.run(ctx, r.Source.Path, "status", "--porcelain=v1", "-z", "--untracked-files=no", "--ignore-submodules=none")
	if e != nil {
		return plan, "", errGit
	}
	if status != "" {
		return plan, "source_dirty", nil
	}
	// Listings are bounded; a repository beyond the bound cannot be planned
	// as a clone and answers a stable reason rather than an unknown error.
	flags, _, e := p.run(ctx, r.Source.Path, "ls-files", "-v", "-z")
	if e != nil {
		return plan, "index_listing_unavailable", nil
	}
	for _, line := range strings.Split(strings.TrimSuffix(flags, "\x00"), "\x00") {
		if line != "" && (len(line) < 3 || line[0] != 'H' || line[1] != ' ') {
			return plan, "source_index_flags", nil
		}
	}
	tree, _, e := p.run(ctx, r.Source.Path, "ls-tree", "-r", "-z", "--full-tree", r.BaseCommit)
	if e != nil {
		return plan, "tree_listing_unavailable", nil
	}
	if tree != "" && !strings.HasSuffix(tree, "\x00") {
		return plan, "", errGit
	}
	for _, line := range strings.Split(strings.TrimSuffix(tree, "\x00"), "\x00") {
		if line == "" {
			continue
		}
		meta, name, ok := strings.Cut(line, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || !treePath(name) {
			return plan, "tree_path_unsupported", nil
		}
		switch fields[0] {
		case "100644", "100755":
			plan.files = append(plan.files, cloneFile{path: name, exec: fields[0] == "100755"})
		case "120000":
			plan.links = append(plan.links, name)
		case "160000":
			return plan, "submodules_unsupported", nil
		default:
			return plan, "tree_entry_unsupported", nil
		}
		if len(plan.files)+len(plan.links) > maxCloneFiles {
			return plan, "tree_too_large", nil
		}
	}
	objects, reason := objectFiles(q.m.root, plan.format)
	if reason != "" {
		return plan, reason, nil
	}
	plan.objects = objects
	return plan, "", nil
}

// openRegular opens name beneath root read-only and proves it is the same
// regular file Lstat observed, so an in-root symlink cannot substitute.
func openRegular(root *os.Root, name string) (*os.File, error) {
	st, e := root.Lstat(name)
	if e != nil || !st.Mode().IsRegular() {
		return nil, errGit
	}
	f, e := root.OpenFile(name, os.O_RDONLY, 0)
	if e != nil {
		return nil, errGit
	}
	actual, e := f.Stat()
	if e != nil || !os.SameFile(st, actual) || !actual.Mode().IsRegular() {
		f.Close()
		return nil, errGit
	}
	return f, nil
}

// probePair clones one object file and one tracked file into owned disposable
// fixtures beneath the attachment base: the actual source/destination pairs.
func (p *Port) probePair(q *roots, plan clonePlan) (bool, string, error) {
	dir, e := q.b.root.Open(".")
	if e != nil {
		return false, "", errGit
	}
	defer dir.Close()
	sources := []struct {
		root *os.Root
		name string
	}{{q.m.root, "objects/" + plan.objects[0]}}
	if len(plan.files) > 0 {
		sources = append(sources, struct {
			root *os.Root
			name string
		}{q.s.root, plan.files[0].path})
	}
	for _, s := range sources {
		f, e := openRegular(s.root, s.name)
		if e != nil {
			return false, "", e
		}
		ok, reason, e := p.cow.probe(f, dir)
		f.Close()
		if e != nil || !ok {
			return false, reason, e
		}
	}
	return true, "", nil
}

// CloneCapability answers the clone method for this exact source/destination
// pair and clean base, or NoClone with a stable reason. It mutates no user or
// repository path.
func (p *Port) CloneCapability(ctx context.Context, r repositories.Request) (repositories.CloneMethod, string, error) {
	if !p.Supported(r) || r.Mode != repositories.Clone || r.Existing || r.Ownership != repositories.Owned || !r.Base.PrivateCustody {
		return repositories.NoClone, "", errGit
	}
	q, e := openRoots(r)
	if e != nil {
		return repositories.NoClone, "", e
	}
	defer q.close()
	if o, e := p.Source(ctx, r); e != nil || !o.Exists {
		return repositories.NoClone, "", errGit
	}
	plan, reason, e := p.planClone(ctx, r, q)
	if e != nil {
		return repositories.NoClone, "", e
	}
	if reason != "" {
		return repositories.NoClone, reason, nil
	}
	ok, reason, e := p.probePair(q, plan)
	if e != nil || !q.valid() {
		return repositories.NoClone, "", errGit
	}
	if !ok {
		return repositories.NoClone, reason, nil
	}
	return p.cow.method(), "", nil
}

// observeClone proves an existing clone owns private metadata: a real .git
// directory that is its own common directory, no alternates, no refused
// configuration and no registration as a worktree of the source.
func (p *Port) observeClone(ctx context.Context, r repositories.Request) (repositories.Observation, error) {
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
	gitDir := filepath.Join(r.Path, ".git")
	gst, e := os.Lstat(gitDir)
	if e != nil || !safeDirectory(gst) || identity(gst) == "" || identity(gst) == r.CommonIdentity {
		return repositories.Observation{}, errGit
	}
	if _, e = canonical(gitDir); e != nil {
		return repositories.Observation{}, errGit
	}
	own, _, e := p.run(ctx, r.Path, "rev-parse", "--path-format=absolute", "--git-dir")
	if e != nil || trim(own) != gitDir {
		return repositories.Observation{}, errGit
	}
	o, e := p.repositoryAt(ctx, r, r.Path, gitDir)
	if e != nil {
		return repositories.Observation{}, e
	}
	if _, registered, e := p.registration(ctx, r); e != nil || registered {
		return repositories.Observation{}, errGit
	}
	if after, e := os.Lstat(gitDir); e != nil || !os.SameFile(gst, after) || !q.valid() {
		return repositories.Observation{}, errGit
	}
	o.CommonIdentity = identity(gst)
	return o, nil
}

type dirCache struct {
	root *os.Root
	open map[string]*os.File
}

func (d *dirCache) get(rel string) (*os.File, error) {
	if f, ok := d.open[rel]; ok {
		return f, nil
	}
	if rel != "." {
		if e := d.root.MkdirAll(rel, 0755); e != nil {
			return nil, e
		}
	}
	st, e := d.root.Lstat(rel)
	if e != nil || !st.IsDir() {
		return nil, errGit
	}
	f, e := d.root.Open(rel)
	if e != nil {
		return nil, e
	}
	actual, e := f.Stat()
	if e != nil || !os.SameFile(st, actual) {
		f.Close()
		return nil, errGit
	}
	d.open[rel] = f
	return f, nil
}
func (d *dirCache) close() {
	for _, f := range d.open {
		f.Close()
	}
}
func (p *Port) cloneInto(src *os.Root, srcName string, dst *dirCache, dstName string, perm fs.FileMode) error {
	f, e := openRegular(src, srcName)
	if e != nil {
		return e
	}
	defer f.Close()
	dir, e := dst.get(path.Dir(dstName))
	if e != nil {
		return e
	}
	return p.cow.clone(f, dir, path.Base(dstName), perm)
}

// createClone constructs the attachment. Before the leaf exists every refusal
// is a no-mutation refusal. After it exists, any failure reports actual
// mutation and the partial clone is retained for recovery; no plain copy is
// substituted and nothing is removed.
func (p *Port) createClone(ctx context.Context, r repositories.Request, validate func(context.Context) error) (repositories.Observation, bool, error) {
	none := repositories.Observation{}
	if !p.Supported(r) || r.Existing || r.Ownership != repositories.Owned || !r.Base.PrivateCustody || validate == nil || r.Clone.Method != p.cow.method() || !r.Clone.Method.CopyOnWrite() && r.Clone.Method != repositories.FixtureCopy {
		return none, false, errGit
	}
	q, e := openRoots(r)
	if e != nil {
		return none, false, e
	}
	defer q.close()
	if o, e := p.Source(ctx, r); e != nil || !o.Exists {
		return none, false, errGit
	}
	if _, e = p.ResolveBase(ctx, r); e != nil || p.BranchAvailable(ctx, r) != nil {
		return none, false, errGit
	}
	if o, e := p.Observe(ctx, r); e != nil || o.Exists {
		return none, false, errGit
	}
	plan, reason, e := p.planClone(ctx, r, q)
	if e != nil || reason != "" {
		return none, false, errGit
	}
	if ok, _, e := p.probePair(q, plan); e != nil || !ok {
		return none, false, errGit
	}
	if ctx.Err() != nil || validate(ctx) != nil || ctx.Err() != nil || !q.valid() {
		return none, false, errGit
	}
	// Recheck after the authority callback; no parent-creation API is used.
	if o, e := p.Observe(ctx, r); e != nil || o.Exists || !q.valid() {
		return none, false, errGit
	}
	name := filepath.Base(r.Path)
	if e = q.b.root.Mkdir(name, 0700); e != nil {
		return none, true, errGit
	}
	created, e := q.b.root.Lstat(name)
	if e != nil || !safeDirectory(created) {
		return none, true, errGit
	}
	leaf, e := os.OpenRoot(r.Path)
	if e != nil {
		return none, true, errGit
	}
	defer leaf.Close()
	if actual, e := leaf.Stat("."); e != nil || !os.SameFile(created, actual) || !q.valid() {
		return none, true, errGit
	}
	if _, _, e = p.run(ctx, r.Path, "init", "--quiet", "--template=", "--object-format="+plan.format, "-b", r.Branch, "--", r.Path); e != nil {
		return none, true, errGit
	}
	// Git honours the umask; private metadata is tightened before any object
	// arrives and must then pass the same owned-directory check as the leaf.
	if gst, e := leaf.Lstat(".git"); e != nil || !gst.IsDir() || leaf.Chmod(".git", 0700) != nil {
		return none, true, errGit
	}
	if gst, e := leaf.Lstat(".git"); e != nil || !safeDirectory(gst) {
		return none, true, errGit
	}
	dst := &dirCache{root: leaf, open: map[string]*os.File{}}
	defer dst.close()
	for _, rel := range plan.objects {
		if ctx.Err() != nil {
			return none, true, errGit
		}
		if e = p.cloneInto(q.m.root, "objects/"+rel, dst, ".git/objects/"+rel, 0444); e != nil {
			return none, true, errGit
		}
	}
	zero := strings.Repeat("0", len(r.BaseCommit))
	if _, _, e = p.run(ctx, r.Path, "update-ref", "--no-deref", "refs/heads/"+r.Branch, r.BaseCommit, zero); e != nil {
		return none, true, errGit
	}
	if head, _, e := p.run(ctx, r.Path, "rev-parse", "--verify", "HEAD^{commit}"); e != nil || trim(head) != r.BaseCommit {
		return none, true, errGit
	}
	if _, _, e = p.run(ctx, r.Path, "rev-list", "--quiet", r.BaseCommit); e != nil {
		return none, true, errGit
	}
	for _, f := range plan.files {
		if ctx.Err() != nil {
			return none, true, errGit
		}
		perm := fs.FileMode(0644)
		if f.exec {
			perm = 0755
		}
		if e = p.cloneInto(q.s.root, f.path, dst, f.path, perm); e != nil {
			return none, true, errGit
		}
	}
	if _, _, e = p.run(ctx, r.Path, "read-tree", r.BaseCommit); e != nil {
		return none, true, errGit
	}
	// Symlinks are absent until now, so checkout-index never overwrites.
	for start := 0; start < len(plan.links); {
		args, size := []string{"checkout-index", "--"}, 0
		for start < len(plan.links) && size+len(plan.links[start]) < maxCheckoutArgs {
			size += len(plan.links[start])
			args = append(args, plan.links[start])
			start++
		}
		if _, _, e = p.run(ctx, r.Path, args...); e != nil {
			return none, true, errGit
		}
	}
	// A fresh index has no stat data: refresh rehashes every cloned file, so a
	// clean status proves the tree equals the pinned base byte for byte.
	if _, _, e = p.run(ctx, r.Path, "update-index", "-q", "--refresh"); e != nil {
		return none, true, errGit
	}
	status, _, e := p.run(ctx, r.Path, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching", "--ignore-submodules=none")
	if e != nil || status != "" {
		return none, true, errGit
	}
	o, e := p.Observe(ctx, r)
	if e != nil || !q.valid() {
		return o, true, errGit
	}
	return o, true, nil
}
