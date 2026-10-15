package gitadapter

import (
	"context"
	"strings"

	"github.com/hollis-labs/substrate/harness/workspace/repositories"
)

// TargetRef observes refs/heads/<branch> in the source and whether any source
// worktree has it checked out. It never mutates.
func (p *Port) TargetRef(ctx context.Context, r repositories.Request, branch string) (string, bool, error) {
	if !p.Supported(r) || r.Mode != repositories.Clone || !repositories.ValidBranch(branch) {
		return "", false, errGit
	}
	q, e := openRoots(r)
	if e != nil {
		return "", false, e
	}
	defer q.close()
	if _, _, e = p.run(ctx, r.Source.Path, "check-ref-format", "--branch", branch); e != nil {
		return "", false, errGit
	}
	raw, code, e := p.run(ctx, r.Source.Path, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	oid := ""
	switch {
	case e == nil:
		if !hexName(trim(raw), len(r.BaseCommit)) {
			return "", false, errGit
		}
		oid = trim(raw)
	case code != 1:
		return "", false, errGit
	}
	list, _, e := p.run(ctx, r.Source.Path, "worktree", "list", "--porcelain", "-z")
	if e != nil {
		return "", false, errGit
	}
	checkedOut := false
	for _, field := range strings.Split(list, "\x00") {
		if field == "branch refs/heads/"+branch {
			checkedOut = true
		}
	}
	if !q.valid() {
		return "", false, errGit
	}
	return oid, checkedOut, nil
}

// MergeBack fetches the clone's branch objects into the source with object
// checking and no ref update, then moves refs/heads/<branch> from exactly
// before to head with a compare-and-swap. Only the clone path is fetched from;
// no refspec, force, tag, submodule, auto-gc or worktree change is involved.
func (p *Port) MergeBack(ctx context.Context, r repositories.Request, branch, before, head string, validate func(context.Context) error) (bool, error) {
	if !p.Supported(r) || r.Mode != repositories.Clone || validate == nil || !repositories.ValidBranch(branch) || !hexName(head, len(r.BaseCommit)) || before != "" && !hexName(before, len(r.BaseCommit)) {
		return false, errGit
	}
	q, e := openRoots(r)
	if e != nil {
		return false, e
	}
	defer q.close()
	if o, e := p.Source(ctx, r); e != nil || !o.Exists {
		return false, errGit
	}
	o, e := p.Observe(ctx, r)
	if e != nil || !o.Exists || o.Head != head || o.Branch != r.Branch {
		return false, errGit
	}
	if target, checkedOut, e := p.TargetRef(ctx, r, branch); e != nil || checkedOut || target != before {
		return false, errGit
	}
	// Fast-forward only: prove ancestry in the clone before any transfer, and
	// again in the source after it.
	for _, ancestor := range []string{r.BaseCommit, before} {
		if ancestor == "" {
			continue
		}
		if _, _, e = p.run(ctx, r.Path, "merge-base", "--is-ancestor", ancestor, head); e != nil {
			return false, errGit
		}
	}
	if ctx.Err() != nil || validate(ctx) != nil || ctx.Err() != nil || !q.valid() {
		return false, errGit
	}
	// From here objects may have been written into the source store.
	if _, _, e = p.run(ctx, r.Source.Path, "-c", "protocol.file.allow=always", "-c", "transfer.fsckObjects=true", "-c", "fetch.fsckObjects=true", "-c", "gc.auto=0", "-c", "maintenance.auto=false", "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--no-auto-gc", "--no-auto-maintenance", "--", r.Path, "refs/heads/"+r.Branch); e != nil {
		return true, errGit
	}
	if _, _, e = p.run(ctx, r.Source.Path, "cat-file", "-e", head+"^{commit}"); e != nil {
		return true, errGit
	}
	for _, ancestor := range []string{r.BaseCommit, before} {
		if ancestor == "" {
			continue
		}
		if _, _, e = p.run(ctx, r.Source.Path, "merge-base", "--is-ancestor", ancestor, head); e != nil {
			return true, errGit
		}
	}
	if ctx.Err() != nil || validate(ctx) != nil || ctx.Err() != nil || !q.valid() {
		return true, errGit
	}
	// Authority refresh is a host callback; recheck the target after it.
	if target, checkedOut, e := p.TargetRef(ctx, r, branch); e != nil || checkedOut || target != before {
		return true, errGit
	}
	old := before
	if old == "" {
		old = strings.Repeat("0", len(r.BaseCommit))
	}
	if _, _, e = p.run(ctx, r.Source.Path, "update-ref", "--no-deref", "-m", "workspace clone merge-back", "refs/heads/"+branch, head, old); e != nil {
		return true, errGit
	}
	return true, nil
}
