package worktree

import (
	"context"
	"sync"
)

// MergedRef identifies the head of a merged pull request.
type MergedRef struct {
	HeadRef string // branch name the PR was opened from
	HeadOID string // commit id of the branch tip when the PR merged
}

// MergedSource lists merged pull requests for a repository. The `gh`-backed
// implementation lives in the ghmerged sub-package.
type MergedSource interface {
	Merged(ctx context.Context, repoRoot string) ([]MergedRef, error)
}

// MergedOption tunes [MergedPR].
type MergedOption func(*mergedPolicy)

// TrustBranchName reaps a worktree whose branch name matches a merged PR's
// head ref without checking commit ids. Branch names are reused, so this can
// reap work added after the merge; it exists for callers that accept that. A
// dirty or locked worktree is still kept.
func TrustBranchName() MergedOption { return func(p *mergedPolicy) { p.trustName = true } }

// MergedPR reaps worktrees whose work has demonstrably shipped in a merged
// pull request. The worktree must be on a branch that matches a merged PR's
// head ref, must not be dirty or locked, and its HEAD must be equal to or an
// ancestor of that PR's HeadOID, which must exist in the local object store
// (fetch first). Anything else is kept. Squash merges still qualify, because
// the test is against the PR's own head commit, not against the base branch.
//
// The policy loads the PR list once, in Prepare, which [Manager.Sweep] calls;
// build a fresh policy (or run Sweep) for each pass. If the source fails, the
// error lands in [Report.Errs] and nothing is reaped.
func MergedPR(src MergedSource, opts ...MergedOption) SweepPolicy {
	p := &mergedPolicy{src: src}
	for _, o := range opts {
		o(p)
	}
	return p
}

type mergedPolicy struct {
	src       MergedSource
	trustName bool

	mu       sync.Mutex
	r        Runner
	repoRoot string
	byRef    map[string][]string // head ref -> head oids
}

func (p *mergedPolicy) Prepare(ctx context.Context, r Runner, repoRoot string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.r, p.repoRoot, p.byRef = r, repoRoot, nil
	if p.src == nil {
		return nil
	}
	prs, err := p.src.Merged(ctx, repoRoot)
	if err != nil {
		return err
	}
	p.byRef = make(map[string][]string, len(prs))
	for _, pr := range prs {
		if pr.HeadRef != "" {
			p.byRef[pr.HeadRef] = append(p.byRef[pr.HeadRef], pr.HeadOID)
		}
	}
	return nil
}

func (p *mergedPolicy) Verdict(ctx context.Context, wt Worktree, st Status) Verdict {
	p.mu.Lock()
	oids, ok := p.byRef[wt.Branch]
	r, repoRoot := p.r, p.repoRoot
	p.mu.Unlock()
	if !ok || wt.Branch == "" || st.Dirty || st.Locked {
		return Keep
	}
	if p.trustName {
		return ReapShipped
	}
	if wt.HEAD == "" || r == nil {
		return Keep
	}
	for _, oid := range oids {
		if oid == "" {
			continue
		}
		if _, err := resolveCommit(ctx, r, repoRoot, oid); err != nil {
			continue // proof commit not present locally
		}
		if _, err := r.Run(ctx, repoRoot, "merge-base", "--is-ancestor", wt.HEAD, oid); err == nil {
			return ReapShipped
		}
	}
	return Keep
}
