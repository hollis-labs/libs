package worktree

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Verdict is a [SweepPolicy]'s decision about one worktree.
type Verdict int

const (
	// Keep leaves the worktree alone.
	Keep Verdict = iota
	// Reap asks for removal under the normal safety checks: a worktree with
	// work in it is still kept, and reported.
	Reap
	// ReapShipped asks for removal on the strength of proof that the work is
	// already on the remote. Ahead/unreachable checks are waived and the
	// branch may be force-deleted; dirty and locked worktrees are still kept.
	// Only return it when the policy has verified the proof itself.
	ReapShipped
)

// SweepPolicy decides which worktrees a sweep tries to reap. Verdict receives
// the worktree as listed and its current [Status].
type SweepPolicy interface {
	Verdict(ctx context.Context, wt Worktree, st Status) Verdict
}

// Preparer is an optional SweepPolicy extension. Sweep calls Prepare once
// before asking for verdicts, so a policy can load remote state (and run git
// through the manager's Runner) a single time per sweep. An error is recorded
// in [Report.Errs]; the sweep continues, and the policy should then answer
// Keep.
type Preparer interface {
	Prepare(ctx context.Context, r Runner, repoRoot string) error
}

// SweepOptions tunes [Manager.Sweep].
type SweepOptions struct {
	// Protected, when it returns true for an id, exempts that worktree from
	// the sweep entirely.
	Protected func(id string) bool
	// Branch is the branch policy for Reap verdicts. The zero value is
	// [DeleteIfMerged]. [DeleteBranch] is rejected: a sweep never
	// force-deletes branches except for [ReapShipped].
	Branch BranchPolicy
}

// Kept explains why a worktree survived a sweep.
type Kept struct {
	ID     string
	Path   string
	Reason string // "policy", "protected", "prunable" or a [RemoveResult.Reason]
}

// Report is the outcome of a sweep.
type Report struct {
	Removed []Worktree
	Kept    []Kept
	// Unregistered lists directories that match the Placement but that git no
	// longer lists as worktrees. They are reported and never deleted.
	Unregistered []string
	Errs         []error
}

// Sweep asks policy about every worktree of the Placement and removes those it
// reaps, subject to the same safety checks as [Manager.Remove] and never with
// Force. It acts only on worktrees git lists; it does not scan for or delete
// unregistered directories. A worktree that policy wants gone but that holds
// work appears in [Report.Kept] with the reason; nothing in this package
// expires such worktrees.
func (m *Manager) Sweep(ctx context.Context, policy SweepPolicy, opts SweepOptions) (Report, error) {
	if policy == nil {
		return Report{}, errors.New("sweep policy required")
	}
	if opts.Branch == DeleteBranch {
		return Report{}, ErrForceRequired
	}
	var rep Report
	if p, ok := policy.(Preparer); ok {
		if err := p.Prepare(ctx, m.r, m.repoRoot); err != nil {
			rep.Errs = append(rep.Errs, fmt.Errorf("prepare sweep policy: %w", err))
		}
	}
	wts, err := m.List(ctx)
	if err != nil {
		return rep, err
	}
	listed := make(map[string]bool, len(wts))
	for _, wt := range wts {
		listed[canon(wt.Path)] = true
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		if opts.Protected != nil && opts.Protected(wt.ID) {
			rep.Kept = append(rep.Kept, Kept{wt.ID, wt.Path, "protected"})
			continue
		}
		if wt.Prunable {
			rep.Kept = append(rep.Kept, Kept{wt.ID, wt.Path, "prunable"})
			continue
		}
		st, err := m.Inspect(ctx, wt)
		if err != nil {
			rep.Kept = append(rep.Kept, Kept{wt.ID, wt.Path, "inspect-failed: " + err.Error()})
			continue
		}
		var res RemoveResult
		switch policy.Verdict(ctx, wt, st) {
		case Reap:
			m.mu.Lock()
			res, err = m.remove(ctx, wt, RemoveOptions{Branch: opts.Branch}, false)
			m.mu.Unlock()
		case ReapShipped:
			m.mu.Lock()
			res, err = m.remove(ctx, wt, RemoveOptions{Branch: DeleteBranch}, true)
			m.mu.Unlock()
		default:
			rep.Kept = append(rep.Kept, Kept{wt.ID, wt.Path, "policy"})
			continue
		}
		switch {
		case err != nil:
			rep.Errs = append(rep.Errs, fmt.Errorf("remove %s: %w", wt.Path, err))
		case res.Removed:
			rep.Removed = append(rep.Removed, wt)
		default:
			rep.Kept = append(rep.Kept, Kept{wt.ID, wt.Path, res.Reason})
		}
	}
	if e, ok := m.pl.(Enumerator); ok {
		for _, p := range e.Candidates(m.repoRoot) {
			if !listed[canon(p)] {
				rep.Unregistered = append(rep.Unregistered, p)
			}
		}
	}
	return rep, nil
}

// TTL reaps worktrees whose directory has not been modified for maxAge. A
// worktree with an unknown modification time is kept. now defaults to
// time.Now. Because removal is safety-checked, an old worktree holding work is
// kept and reported, not expired.
func TTL(maxAge time.Duration, now func() time.Time) SweepPolicy {
	if now == nil {
		now = time.Now
	}
	return ttlPolicy{maxAge: maxAge, now: now}
}

type ttlPolicy struct {
	maxAge time.Duration
	now    func() time.Time
}

func (p ttlPolicy) Verdict(_ context.Context, wt Worktree, _ Status) Verdict {
	if p.maxAge <= 0 || wt.ModTime.IsZero() {
		return Keep
	}
	if p.now().Sub(wt.ModTime) > p.maxAge {
		return Reap
	}
	return Keep
}

// OrphanedBy reaps every worktree whose id is not marked true in active. A nil
// or empty map therefore asks for everything to be reaped; the safety checks
// still keep each worktree that holds work.
func OrphanedBy(active map[string]bool) SweepPolicy { return orphanPolicy{active: active} }

type orphanPolicy struct{ active map[string]bool }

func (p orphanPolicy) Verdict(_ context.Context, wt Worktree, _ Status) Verdict {
	if p.active[wt.ID] {
		return Keep
	}
	return Reap
}

// Any combines policies: the strongest verdict wins (ReapShipped over Reap
// over Keep). It forwards [Preparer] to the members that implement it.
func Any(policies ...SweepPolicy) SweepPolicy { return anyPolicy(policies) }

type anyPolicy []SweepPolicy

func (a anyPolicy) Prepare(ctx context.Context, r Runner, repoRoot string) error {
	var errs []error
	for _, p := range a {
		if pp, ok := p.(Preparer); ok {
			if err := pp.Prepare(ctx, r, repoRoot); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (a anyPolicy) Verdict(ctx context.Context, wt Worktree, st Status) Verdict {
	best := Keep
	for _, p := range a {
		if v := p.Verdict(ctx, wt, st); v > best {
			best = v
		}
	}
	return best
}
