package worktree_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	worktree "github.com/hollis-labs/libs/util/worktree"
)

type fakeSource struct {
	prs []worktree.MergedRef
	err error
}

func (f fakeSource) Merged(context.Context, string) ([]worktree.MergedRef, error) {
	return f.prs, f.err
}

// shippedFixture makes a worktree on branch fix/CW-1 with one commit and
// returns the commit id, i.e. the PR head a squash merge would have recorded.
func shippedFixture(t *testing.T) (*worktree.Manager, worktree.Worktree, string, string) {
	t.Helper()
	repo := newOriginClone(t)
	m := mustNew(t, repo)
	wt := mustCreate(t, m, worktree.Spec{ID: "pr"})
	git(t, wt.Path, "checkout", "-q", "-b", "fix/CW-1")
	sha := commitFile(t, wt.Path, "code.go", "package x")
	return m, wt, sha, repo
}

func sweepMerged(t *testing.T, m *worktree.Manager, src worktree.MergedSource, opts ...worktree.MergedOption) worktree.Report {
	t.Helper()
	rep, err := m.Sweep(ctx, worktree.MergedPR(src, opts...), worktree.SweepOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// TestMergedPR_ReapsWithOIDEvidence is acceptance (f), positive half: a
// squash-merged worktree that plain removal would preserve as "ahead of base"
// is reaped when its HEAD is the merged PR's head, and the branch (which
// `branch -d` would refuse) is deleted.
func TestMergedPR_ReapsWithOIDEvidence(t *testing.T) {
	m, wt, sha, repo := shippedFixture(t)

	if res, err := m.Remove(ctx, wt, worktree.RemoveOptions{}); err != nil || res.Removed || res.Reason != "ahead-of-base" {
		t.Fatalf("precondition: plain Remove = %+v, %v; want kept ahead-of-base", res, err)
	}
	rep := sweepMerged(t, m, fakeSource{prs: []worktree.MergedRef{{HeadRef: "fix/CW-1", HeadOID: sha}}})
	if len(rep.Removed) != 1 || len(rep.Errs) != 0 {
		t.Fatalf("Report = %+v", rep)
	}
	if exists(wt.Path) || hasBranch(t, repo, "fix/CW-1") {
		t.Error("worktree or branch survived a proven merge")
	}
}

func TestMergedPR_AncestorOfHeadOIDIsEnough(t *testing.T) {
	m, wt, sha, _ := shippedFixture(t)
	// The PR head advanced past what this worktree has: build a descendant
	// commit in a scratch worktree, then drop the worktree, leaving the
	// commit unreferenced but present in the object store.
	tmp := filepath.Join(t.TempDir(), "scratch")
	git(t, m.RepoRoot(), "worktree", "add", "-q", "--detach", tmp, sha)
	desc := commitFile(t, tmp, "more.txt", "m")
	git(t, m.RepoRoot(), "worktree", "remove", "--force", tmp)
	rep := sweepMerged(t, m, fakeSource{prs: []worktree.MergedRef{{HeadRef: "fix/CW-1", HeadOID: desc}}})
	if len(rep.Removed) != 1 {
		t.Fatalf("Report = %+v; HEAD is an ancestor of HeadOID and should qualify", rep)
	}
	if exists(wt.Path) {
		t.Error("worktree survived")
	}
}

// TestMergedPR_RefusesWhenHeadNotAncestor is acceptance (f), second clause:
// work added after the merge (or a reused branch name) must not be destroyed.
func TestMergedPR_RefusesWhenHeadNotAncestor(t *testing.T) {
	m, wt, sha, repo := shippedFixture(t)
	commitFile(t, wt.Path, "after-merge.go", "package y") // new work after the PR head

	rep := sweepMerged(t, m, fakeSource{prs: []worktree.MergedRef{{HeadRef: "fix/CW-1", HeadOID: sha}}})
	if len(rep.Removed) != 0 || !exists(wt.Path) || !hasBranch(t, repo, "fix/CW-1") {
		t.Fatalf("post-merge work destroyed: %+v", rep)
	}
}

func TestMergedPR_RefusesReusedBranchNameWithUnrelatedOID(t *testing.T) {
	m, wt, _, repo := shippedFixture(t)
	unrelated := git(t, repo, "rev-parse", "origin/main") // exists locally, not a descendant of wt HEAD
	rep := sweepMerged(t, m, fakeSource{prs: []worktree.MergedRef{{HeadRef: "fix/CW-1", HeadOID: unrelated}}})
	if len(rep.Removed) != 0 || !exists(wt.Path) {
		t.Fatalf("unrelated OID accepted as proof: %+v", rep)
	}
}

func TestMergedPR_RefusesUnknownOIDAndEmptyOID(t *testing.T) {
	m, wt, _, _ := shippedFixture(t)
	for _, oid := range []string{"", "0123456789abcdef0123456789abcdef01234567"} {
		rep := sweepMerged(t, m, fakeSource{prs: []worktree.MergedRef{{HeadRef: "fix/CW-1", HeadOID: oid}}})
		if len(rep.Removed) != 0 || !exists(wt.Path) {
			t.Fatalf("OID %q accepted: %+v", oid, rep)
		}
	}
}

// TestMergedPR_RefusesDirty is acceptance (f), third clause.
func TestMergedPR_RefusesDirty(t *testing.T) {
	m, wt, sha, _ := shippedFixture(t)
	write(t, filepath.Join(wt.Path, "wip.txt"), "unsaved")
	rep := sweepMerged(t, m, fakeSource{prs: []worktree.MergedRef{{HeadRef: "fix/CW-1", HeadOID: sha}}})
	if len(rep.Removed) != 0 || !exists(filepath.Join(wt.Path, "wip.txt")) {
		t.Fatalf("dirty merged worktree destroyed: %+v", rep)
	}
	// Even trusting the branch name never removes dirty work.
	rep = sweepMerged(t, m, fakeSource{prs: []worktree.MergedRef{{HeadRef: "fix/CW-1", HeadOID: sha}}}, worktree.TrustBranchName())
	if len(rep.Removed) != 0 || !exists(wt.Path) {
		t.Fatalf("TrustBranchName removed dirty work: %+v", rep)
	}
}

func TestMergedPR_RefusesLocked(t *testing.T) {
	m, wt, sha, repo := shippedFixture(t)
	git(t, repo, "worktree", "lock", wt.Path)
	rep := sweepMerged(t, m, fakeSource{prs: []worktree.MergedRef{{HeadRef: "fix/CW-1", HeadOID: sha}}})
	if len(rep.Removed) != 0 || !exists(wt.Path) {
		t.Fatalf("locked worktree removed: %+v", rep)
	}
}

func TestMergedPR_TrustBranchName(t *testing.T) {
	m, wt, _, repo := shippedFixture(t)
	rep := sweepMerged(t, m, fakeSource{prs: []worktree.MergedRef{{HeadRef: "fix/CW-1"}}}, worktree.TrustBranchName())
	if len(rep.Removed) != 1 || exists(wt.Path) || hasBranch(t, repo, "fix/CW-1") {
		t.Fatalf("TrustBranchName did not reap: %+v", rep)
	}
}

func TestMergedPR_KeepsUnmergedAndDetached(t *testing.T) {
	m, open, sha, _ := shippedFixture(t)
	det := mustCreate(t, m, worktree.Spec{ID: "det"})
	rep := sweepMerged(t, m, fakeSource{prs: []worktree.MergedRef{
		{HeadRef: "some/other-branch", HeadOID: sha},
		{HeadRef: "", HeadOID: sha},
	}})
	if len(rep.Removed) != 0 || !exists(open.Path) || !exists(det.Path) {
		t.Fatalf("Report = %+v", rep)
	}
}

// TestMergedPR_SourceFailureReapsNothing: Torque swallowed gh failures and
// carried on; here the failure is reported and nothing is removed.
func TestMergedPR_SourceFailureReapsNothing(t *testing.T) {
	m, wt, _, _ := shippedFixture(t)
	boom := errors.New("gh exploded")
	rep := sweepMerged(t, m, fakeSource{err: boom})
	if len(rep.Removed) != 0 || !exists(wt.Path) {
		t.Fatalf("removed despite source failure: %+v", rep)
	}
	if len(rep.Errs) != 1 || !errors.Is(rep.Errs[0], boom) {
		t.Errorf("Errs = %v, want the source error", rep.Errs)
	}
	// nil source and empty list are no-ops.
	for _, src := range []worktree.MergedSource{nil, fakeSource{}} {
		if rep := sweepMerged(t, m, src); len(rep.Removed) != 0 || len(rep.Errs) != 0 || !exists(wt.Path) {
			t.Fatalf("source %v: %+v", src, rep)
		}
	}
}

// TestMergedPR_InsideAnyKeepsOtherPoliciesWorking: a failing merged source must
// not disable the other member of Any.
func TestMergedPR_InsideAnyKeepsOtherPoliciesWorking(t *testing.T) {
	m, wt, _, _ := shippedFixture(t)
	clean := mustCreate(t, m, worktree.Spec{ID: "cleanorphan"})
	rep, err := m.Sweep(ctx, worktree.Any(
		worktree.MergedPR(fakeSource{err: errors.New("down")}),
		worktree.OrphanedBy(map[string]bool{"pr": true}),
	), worktree.SweepOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errs) != 1 || exists(clean.Path) || !exists(wt.Path) {
		t.Fatalf("Report = %+v", rep)
	}
}

// hookPolicy runs hook inside Verdict, then answers ReapShipped: it models the
// worktree changing between the verdict and the removal.
type hookPolicy struct{ hook func(wt worktree.Worktree) }

func (h hookPolicy) Verdict(_ context.Context, wt worktree.Worktree, _ worktree.Status) worktree.Verdict {
	h.hook(wt)
	return worktree.ReapShipped
}

func assertChangedSinceVerdict(t *testing.T, rep worktree.Report, wt worktree.Worktree, repo string, branches ...string) {
	t.Helper()
	if len(rep.Removed) != 0 || len(rep.Errs) != 0 {
		t.Fatalf("Report = %+v; nothing may be removed", rep)
	}
	if len(rep.Kept) != 1 || rep.Kept[0].Reason != "changed-since-verdict" {
		t.Fatalf("Kept = %+v, want changed-since-verdict", rep.Kept)
	}
	if !exists(wt.Path) {
		t.Fatal("worktree deleted")
	}
	for _, b := range branches {
		if !hasBranch(t, repo, b) {
			t.Errorf("branch %s deleted", b)
		}
	}
}

// TestSweep_ReapShippedKeepsWorktreeThatGainedCommit: a commit lands after the
// proof was computed; the tree is clean, so only the re-check saves it.
func TestSweep_ReapShippedKeepsWorktreeThatGainedCommit(t *testing.T) {
	m, wt, _, repo := shippedFixture(t)
	var late string
	rep, err := m.Sweep(ctx, hookPolicy{hook: func(w worktree.Worktree) {
		late = commitFile(t, w.Path, "late.go", "package late")
	}}, worktree.SweepOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertChangedSinceVerdict(t, rep, wt, repo, "fix/CW-1")
	if git(t, repo, "rev-parse", "fix/CW-1") != late {
		t.Error("branch no longer points at the late commit")
	}
}

// TestSweep_ReapShippedKeepsWorktreeThatSwitchedBranch: the branch proved
// shipped is not the branch checked out at removal time.
func TestSweep_ReapShippedKeepsWorktreeThatSwitchedBranch(t *testing.T) {
	m, wt, _, repo := shippedFixture(t)
	rep, err := m.Sweep(ctx, hookPolicy{hook: func(w worktree.Worktree) {
		git(t, w.Path, "checkout", "-q", "-b", "other/work")
	}}, worktree.SweepOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Same commit, different branch: HEAD is unchanged, branch differs.
	assertChangedSinceVerdict(t, rep, wt, repo, "fix/CW-1", "other/work")
}

// TestSweep_ReapShippedUnchangedStillReaps guards against the re-check being
// too eager.
func TestSweep_ReapShippedUnchangedStillReaps(t *testing.T) {
	m, wt, _, repo := shippedFixture(t)
	rep, err := m.Sweep(ctx, hookPolicy{hook: func(worktree.Worktree) {}}, worktree.SweepOptions{})
	if err != nil || len(rep.Removed) != 1 || exists(wt.Path) || hasBranch(t, repo, "fix/CW-1") {
		t.Fatalf("%+v, %v", rep, err)
	}
}
