package worktree_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	worktree "github.com/hollis-labs/libs/util/worktree"
)

var ctx = context.Background()

func TestNew_ValidatesRepoRoot(t *testing.T) {
	if _, err := worktree.New(t.TempDir()); !errors.Is(err, worktree.ErrNotRepo) {
		t.Errorf("non-repo dir: err = %v, want ErrNotRepo", err)
	}
	repo := newRepo(t)
	sub := filepath.Join(repo, "pkg")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.New(sub); !errors.Is(err, worktree.ErrNotRepo) {
		t.Errorf("subdirectory: err = %v, want ErrNotRepo (New needs the top level)", err)
	}
	m, err := worktree.New(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !same(m.RepoRoot(), repo) {
		t.Errorf("RepoRoot = %q, want %q", m.RepoRoot(), repo)
	}
}

func TestNew_IgnoresProcessCwd(t *testing.T) {
	repo := newRepo(t)
	other := newRepo(t)
	t.Chdir(other)
	m := mustNew(t, repo)
	wt := mustCreate(t, m, worktree.Spec{ID: "a"})
	if got := git(t, wt.Path, "rev-parse", "--git-common-dir"); !same(got, filepath.Join(repo, ".git")) {
		t.Errorf("worktree belongs to %q, want the explicit repo %q", got, repo)
	}
}

func TestFindRepoRoot(t *testing.T) {
	repo := newRepo(t)
	deep := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(deep, 0o750); err != nil {
		t.Fatal(err)
	}
	got, err := worktree.FindRepoRoot(deep)
	if err != nil || got != repo {
		t.Errorf("FindRepoRoot = %q, %v; want %q", got, err, repo)
	}
	// A linked worktree has a .git file, which counts.
	wt := mustCreate(t, mustNew(t, repo), worktree.Spec{ID: "x"})
	if got, err := worktree.FindRepoRoot(wt.Path); err != nil || got != wt.Path {
		t.Errorf("FindRepoRoot(linked worktree) = %q, %v; want %q", got, err, wt.Path)
	}
	if _, err := worktree.FindRepoRoot(t.TempDir()); err == nil {
		t.Error("expected an error outside any repository")
	}
}

func TestCreate_DetachedAtBaseAndList(t *testing.T) {
	repo := newRepo(t)
	m := mustNew(t, repo)
	wt := mustCreate(t, m, worktree.Spec{ID: "one"})

	want := filepath.Join(filepath.Dir(m.RepoRoot()), filepath.Base(repo)+"-worktrees-one")
	if wt.Path != want {
		t.Errorf("Path = %q, want %q", wt.Path, want)
	}
	if wt.Branch != "" {
		t.Errorf("Branch = %q, want detached", wt.Branch)
	}
	if head := git(t, repo, "rev-parse", "HEAD"); wt.HEAD != head {
		t.Errorf("HEAD = %s, want %s", wt.HEAD, head)
	}
	if git(t, wt.Path, "rev-parse", "--abbrev-ref", "HEAD") != "HEAD" {
		t.Error("worktree is not detached")
	}

	list, err := m.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "one" || list[0].Path != wt.Path || list[0].Branch != "" || list[0].HEAD != wt.HEAD {
		t.Errorf("List = %+v", list)
	}
	if list[0].ModTime.IsZero() {
		t.Error("ModTime not set")
	}
}

func TestCreate_BranchPrecedence(t *testing.T) {
	repo := newRepo(t)
	m := mustNew(t, repo, worktree.WithBranchNamer(func(id string) string { return "auto-" + id }))

	if wt := mustCreate(t, m, worktree.Spec{ID: "a"}); wt.Branch != "auto-a" || !hasBranch(t, repo, "auto-a") {
		t.Errorf("namer branch: %+v", wt)
	}
	if wt := mustCreate(t, m, worktree.Spec{ID: "b", Branch: "explicit"}); wt.Branch != "explicit" || !hasBranch(t, repo, "explicit") {
		t.Errorf("explicit branch: %+v", wt)
	}
	wt := mustCreate(t, m, worktree.Spec{ID: "c", Detached: true})
	if wt.Branch != "" || hasBranch(t, repo, "auto-c") {
		t.Errorf("Detached must beat the namer: %+v", wt)
	}
}

func TestCreate_RejectsBadBranchAndBase(t *testing.T) {
	m := mustNew(t, newRepo(t))
	for _, b := range []string{"-evil", "a b", "x..y", "bad~name"} {
		if _, err := m.Create(ctx, worktree.Spec{ID: "b", Branch: b}); err == nil {
			t.Errorf("branch %q accepted", b)
		}
	}
	if _, err := m.Create(ctx, worktree.Spec{ID: "b", BaseRef: "no-such-ref"}); err == nil {
		t.Error("unresolvable BaseRef accepted")
	}
	if _, err := m.Create(ctx, worktree.Spec{ID: "b", BaseRef: "--help"}); err == nil {
		t.Error("option-like BaseRef accepted")
	}
}

func TestCreate_SpecBaseRef(t *testing.T) {
	repo := newRepo(t)
	first := git(t, repo, "rev-parse", "HEAD")
	commitFile(t, repo, "later.txt", "x")
	m := mustNew(t, repo)
	wt := mustCreate(t, m, worktree.Spec{ID: "old", BaseRef: first})
	if wt.HEAD != first {
		t.Errorf("HEAD = %s, want %s", wt.HEAD, first)
	}
}

// TestCreate_ExistingPathRefused pins Tether's collision guard: refuse a
// pre-existing path with an actionable message, both for a leaked directory
// and for a registered-but-deleted worktree.
func TestCreate_ExistingPathRefused(t *testing.T) {
	repo := newRepo(t)
	m := mustNew(t, repo)
	wt := mustCreate(t, m, worktree.Spec{ID: "dup"})

	_, err := m.Create(ctx, worktree.Spec{ID: "dup"})
	if !errors.Is(err, worktree.ErrPathExists) {
		t.Fatalf("err = %v, want ErrPathExists", err)
	}
	for _, hint := range []string{"already exists", "worktree remove --force", "worktree prune"} {
		if !strings.Contains(err.Error(), hint) {
			t.Errorf("error %q lacks hint %q", err, hint)
		}
	}

	// Registered path whose directory vanished: git refuses; the error is framed.
	if rmErr := os.RemoveAll(wt.Path); rmErr != nil {
		t.Fatal(rmErr)
	}
	_, err = m.Create(ctx, worktree.Spec{ID: "dup"})
	if err == nil {
		t.Fatal("expected an error for a registered path")
	}
	if !strings.Contains(err.Error(), "worktree prune") || !strings.Contains(err.Error(), "worktree") {
		t.Errorf("registered-path error not framed: %v", err)
	}
}

// TestCreate_RejectsUnsafeIDs covers acceptance (h).
func TestCreate_RejectsUnsafeIDs(t *testing.T) {
	repo := newRepo(t)
	m := mustNew(t, repo)
	bad := []string{"", "..", ".", "a/b", `a\b`, "a\x00b", "../x", "x/..", "-rf", "a\nb", "a..b", strings.Repeat("x", 201)}
	for _, id := range bad {
		if _, err := m.Create(ctx, worktree.Spec{ID: id}); !errors.Is(err, worktree.ErrInvalidID) {
			t.Errorf("id %q: err = %v, want ErrInvalidID", id, err)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(m.RepoRoot()))
	for _, e := range entries {
		if strings.Contains(e.Name(), "-worktrees-") {
			t.Errorf("rejected id still created %s", e.Name())
		}
	}
}

func TestCreate_GuardVeto(t *testing.T) {
	repo := newRepo(t)
	veto := errors.New("veto")
	m := mustNew(t, repo, worktree.WithGuard(func(context.Context, string, string) error { return veto }))
	if _, err := m.Create(ctx, worktree.Spec{ID: "g"}); !errors.Is(err, veto) {
		t.Errorf("err = %v, want veto", err)
	}
	if exists(m.RepoRoot() + "-worktrees-g") {
		t.Error("vetoed worktree was created")
	}
}

func TestCreate_SpecPathOverride(t *testing.T) {
	repo := newRepo(t)
	m := mustNew(t, repo)
	p := filepath.Join(t.TempDir(), "custom", "here")
	wt := mustCreate(t, m, worktree.Spec{ID: "c", Path: p})
	if wt.Path != p || !exists(filepath.Join(p, "README.md")) {
		t.Errorf("Path override ignored: %+v", wt)
	}
	if list, _ := m.List(ctx); len(list) != 0 {
		t.Errorf("List = %+v; an override path outside the placement must not be listed", list)
	}
	res, err := m.Remove(ctx, wt, worktree.RemoveOptions{})
	if err != nil || !res.Removed {
		t.Errorf("Remove of override worktree: %+v, %v", res, err)
	}
}

func TestInspect_Status(t *testing.T) {
	repo := newOriginClone(t)
	m := mustNew(t, repo)
	wt := mustCreate(t, m, worktree.Spec{ID: "s"})

	st, err := m.Inspect(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	if st != (worktree.Status{}) {
		t.Errorf("fresh worktree status = %+v, want zero", st)
	}

	write(t, filepath.Join(wt.Path, "wip.txt"), "x")
	if st, _ = m.Inspect(ctx, wt); !st.Dirty {
		t.Error("untracked file not reported as Dirty")
	}
	if err := os.Remove(filepath.Join(wt.Path, "wip.txt")); err != nil {
		t.Fatal(err)
	}

	commitFile(t, wt.Path, "c.txt", "c")
	st, _ = m.Inspect(ctx, wt)
	if st.UnreachableCommits != 1 || st.AheadOfBase != 1 || st.Branch != "" || st.Dirty {
		t.Errorf("detached commit status = %+v", st)
	}

	git(t, wt.Path, "checkout", "-q", "-b", "task/x")
	st, _ = m.Inspect(ctx, wt)
	if st.UnreachableCommits != 0 || st.AheadOfBase != 1 || st.Branch != "task/x" {
		t.Errorf("branch commit status = %+v", st)
	}

	if _, err := m.Inspect(ctx, worktree.Worktree{Path: filepath.Join(t.TempDir(), "nope")}); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing path: err = %v, want ErrNotExist", err)
	}
}

func TestInspect_AheadOfBaseUnresolved(t *testing.T) {
	repo := newRepo(t)
	m := mustNew(t, repo, worktree.WithBaseRef(worktree.FixedBase("refs/heads/does-not-exist")))
	wt := mustCreate(t, m, worktree.Spec{ID: "u", BaseRef: "HEAD"})
	st, err := m.Inspect(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	if st.AheadOfBase != -1 {
		t.Errorf("AheadOfBase = %d, want -1", st.AheadOfBase)
	}
}

func TestRemove_CleanWorktreeIsRemoved(t *testing.T) {
	repo := newRepo(t)
	m := mustNew(t, repo)
	wt := mustCreate(t, m, worktree.Spec{ID: "clean"})
	res, err := m.Remove(ctx, wt, worktree.RemoveOptions{})
	if err != nil || !res.Removed || res.Reason != "" {
		t.Fatalf("Remove = %+v, %v", res, err)
	}
	if exists(wt.Path) {
		t.Error("directory still present")
	}
	if list, _ := m.List(ctx); len(list) != 0 {
		t.Errorf("still listed: %+v", list)
	}
}

func TestRemove_AbsentIsIdempotentAndPrunes(t *testing.T) {
	repo := newRepo(t)
	m := mustNew(t, repo)
	wt := mustCreate(t, m, worktree.Spec{ID: "gone"})
	if err := os.RemoveAll(wt.Path); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		res, err := m.Remove(ctx, wt, worktree.RemoveOptions{})
		if err != nil || res.Removed || res.Reason != "absent" {
			t.Fatalf("call %d: %+v, %v", i, res, err)
		}
	}
	if strings.Contains(git(t, repo, "worktree", "list"), "-worktrees-gone") {
		t.Error("stale registration was not pruned")
	}
	// The id is reusable afterwards.
	mustCreate(t, m, worktree.Spec{ID: "gone"})
}

// TestRemove_NoOriginDetachedCommitKept is acceptance (a): Torque's guard
// treated a failed `rev-list origin/main..HEAD` as "no work" in a repo with no
// origin and removed a clean detached worktree holding a commit, orphaning it.
func TestRemove_NoOriginDetachedCommitKept(t *testing.T) {
	repo := newRepo(t)
	m := mustNew(t, repo)
	wt := mustCreate(t, m, worktree.Spec{ID: "a"})
	sha := commitFile(t, wt.Path, "agent.go", "package x")

	res, err := m.Remove(ctx, wt, worktree.RemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed || res.Reason != "unreachable-commits" || res.Status.UnreachableCommits != 1 {
		t.Fatalf("Remove = %+v, want kept for unreachable-commits", res)
	}
	if !exists(wt.Path) {
		t.Fatal("worktree directory was deleted")
	}
	git(t, repo, "cat-file", "-e", sha+"^{commit}")
}

// TestRemove_DirtyKept is acceptance (b).
func TestRemove_DirtyKept(t *testing.T) {
	for name, mutate := range map[string]func(t *testing.T, wt worktree.Worktree){
		"untracked": func(t *testing.T, wt worktree.Worktree) { write(t, filepath.Join(wt.Path, "scratch.txt"), "wip") },
		"modified":  func(t *testing.T, wt worktree.Worktree) { write(t, filepath.Join(wt.Path, "README.md"), "changed") },
		"staged": func(t *testing.T, wt worktree.Worktree) {
			write(t, filepath.Join(wt.Path, "new.txt"), "n")
			git(t, wt.Path, "add", "new.txt")
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := mustNew(t, newRepo(t))
			wt := mustCreate(t, m, worktree.Spec{ID: "d"})
			mutate(t, wt)
			res, err := m.Remove(ctx, wt, worktree.RemoveOptions{})
			if err != nil || res.Removed || res.Reason != "dirty" || !res.Status.Dirty {
				t.Fatalf("Remove = %+v, %v", res, err)
			}
			if !exists(wt.Path) {
				t.Error("dirty worktree deleted")
			}
		})
	}
}

// TestRemove_LockedKept is acceptance (c): a locked worktree survives, even
// with Force, and the library never double-forces.
func TestRemove_LockedKept(t *testing.T) {
	repo := newRepo(t)
	m := mustNew(t, repo)
	wt := mustCreate(t, m, worktree.Spec{ID: "l"})
	git(t, repo, "worktree", "lock", "--reason", "in use", wt.Path)

	for _, force := range []bool{false, true} {
		res, err := m.Remove(ctx, wt, worktree.RemoveOptions{Force: force})
		if err != nil || res.Removed || res.Reason != "locked" || !res.Status.Locked {
			t.Fatalf("Force=%v: Remove = %+v, %v", force, res, err)
		}
		if !exists(wt.Path) {
			t.Fatalf("Force=%v: locked worktree deleted", force)
		}
	}
	list, _ := m.List(ctx)
	if len(list) != 1 || !list[0].Locked {
		t.Errorf("List = %+v, want one locked worktree", list)
	}

	git(t, repo, "worktree", "unlock", wt.Path)
	if res, err := m.Remove(ctx, wt, worktree.RemoveOptions{}); err != nil || !res.Removed {
		t.Errorf("after unlock: %+v, %v", res, err)
	}
}

func TestRemove_AheadOfBaseKeptOnBranch(t *testing.T) {
	repo := newOriginClone(t)
	m := mustNew(t, repo)
	wt := mustCreate(t, m, worktree.Spec{ID: "b"})
	git(t, wt.Path, "checkout", "-q", "-b", "task/CW-test")
	commitFile(t, wt.Path, "agent.go", "package x")

	res, err := m.Remove(ctx, wt, worktree.RemoveOptions{})
	if err != nil || res.Removed || res.Reason != "ahead-of-base" {
		t.Fatalf("Remove = %+v, %v", res, err)
	}
	if !exists(wt.Path) {
		t.Fatal("worktree deleted")
	}
}

// TestRemove_ForcedBranchWorktreeKeepsUnmergedBranch is acceptance (e): with
// Force the worktree goes, but DeleteIfMerged uses `git branch -d`, which git
// refuses for an unmerged branch, so the branch and its commit survive.
func TestRemove_ForcedBranchWorktreeKeepsUnmergedBranch(t *testing.T) {
	repo := newOriginClone(t)
	m := mustNew(t, repo)
	wt := mustCreate(t, m, worktree.Spec{ID: "e"})
	git(t, wt.Path, "checkout", "-q", "-b", "task/keep-me")
	sha := commitFile(t, wt.Path, "agent.go", "package x")

	res, err := m.Remove(ctx, wt, worktree.RemoveOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Removed || res.BranchDeleted || res.BranchNote == "" {
		t.Fatalf("Remove = %+v; want removed, branch kept with a note", res)
	}
	if exists(wt.Path) {
		t.Error("directory still present")
	}
	if !hasBranch(t, repo, "task/keep-me") || git(t, repo, "rev-parse", "task/keep-me") != sha {
		t.Error("unmerged branch or its commit was lost")
	}
}

func TestRemove_BranchPolicies(t *testing.T) {
	setup := func(t *testing.T, merged bool) (*worktree.Manager, worktree.Worktree, string) {
		repo := newRepo(t)
		m := mustNew(t, repo, worktree.WithBranchNamer(func(id string) string { return "b-" + id }))
		wt := mustCreate(t, m, worktree.Spec{ID: "p"})
		if !merged {
			commitFile(t, wt.Path, "x.txt", "x")
		}
		return m, wt, repo
	}

	t.Run("DeleteIfMerged deletes a merged branch", func(t *testing.T) {
		m, wt, repo := setup(t, true)
		res, err := m.Remove(ctx, wt, worktree.RemoveOptions{})
		if err != nil || !res.Removed || !res.BranchDeleted || hasBranch(t, repo, "b-p") {
			t.Fatalf("%+v, %v", res, err)
		}
	})
	t.Run("KeepBranch", func(t *testing.T) {
		m, wt, repo := setup(t, true)
		res, err := m.Remove(ctx, wt, worktree.RemoveOptions{Branch: worktree.KeepBranch})
		if err != nil || !res.Removed || res.BranchDeleted || !hasBranch(t, repo, "b-p") {
			t.Fatalf("%+v, %v", res, err)
		}
	})
	t.Run("DeleteBranch needs Force", func(t *testing.T) {
		m, wt, repo := setup(t, false)
		if _, err := m.Remove(ctx, wt, worktree.RemoveOptions{Branch: worktree.DeleteBranch}); !errors.Is(err, worktree.ErrForceRequired) {
			t.Fatalf("err = %v, want ErrForceRequired", err)
		}
		if !exists(wt.Path) || !hasBranch(t, repo, "b-p") {
			t.Fatal("rejected call had side effects")
		}
		res, err := m.Remove(ctx, wt, worktree.RemoveOptions{Force: true, Branch: worktree.DeleteBranch})
		if err != nil || !res.Removed || !res.BranchDeleted || hasBranch(t, repo, "b-p") {
			t.Fatalf("forced delete: %+v, %v", res, err)
		}
	})
}

func TestRemove_ForceRemovesWork(t *testing.T) {
	m := mustNew(t, newRepo(t))
	wt := mustCreate(t, m, worktree.Spec{ID: "f"})
	write(t, filepath.Join(wt.Path, "dirty.txt"), "x")
	commitFile(t, wt.Path, "c.txt", "c")
	res, err := m.Remove(ctx, wt, worktree.RemoveOptions{Force: true})
	if err != nil || !res.Removed || exists(wt.Path) {
		t.Fatalf("Force did not remove: %+v, %v", res, err)
	}
}

func TestRemove_InspectFailureKeeps(t *testing.T) {
	repo := newRepo(t)
	m := mustNew(t, repo)
	wt := mustCreate(t, m, worktree.Spec{ID: "i"})
	// Corrupt the worktree's link to its repository so git commands fail there.
	write(t, filepath.Join(wt.Path, ".git"), "gitdir: /nonexistent/nowhere\n")

	res, err := m.Remove(ctx, wt, worktree.RemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed || !strings.HasPrefix(res.Reason, "inspect-failed") {
		t.Fatalf("Remove = %+v, want kept with inspect-failed", res)
	}
	if !exists(wt.Path) {
		t.Fatal("uninspectable worktree deleted")
	}
}

// TestSweep_OrphanedByNilKeepsWork is acceptance (d): Nanite's startup
// CleanupOrphaned(nil) force-deleted every worktree, dirty or not.
func TestSweep_OrphanedByNilKeepsWork(t *testing.T) {
	repo := newRepo(t)
	m := mustNew(t, repo, worktree.WithBranchNamer(func(id string) string { return "w-" + id }))

	clean := mustCreate(t, m, worktree.Spec{ID: "clean"})
	dirty := mustCreate(t, m, worktree.Spec{ID: "dirty"})
	write(t, filepath.Join(dirty.Path, "wip.txt"), "x")
	committed := mustCreate(t, m, worktree.Spec{ID: "committed", Detached: true})
	commitFile(t, committed.Path, "c.txt", "c")
	onBranch := mustCreate(t, m, worktree.Spec{ID: "onbranch"})
	commitFile(t, onBranch.Path, "b.txt", "b")

	rep, err := m.Sweep(ctx, worktree.OrphanedBy(nil), worktree.SweepOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errs) != 0 {
		t.Fatalf("Errs = %v", rep.Errs)
	}
	if len(rep.Removed) != 1 || rep.Removed[0].ID != "clean" || exists(clean.Path) {
		t.Fatalf("Removed = %+v; only the clean worktree may go", rep.Removed)
	}
	reasons := map[string]string{}
	for _, k := range rep.Kept {
		reasons[k.ID] = k.Reason
	}
	want := map[string]string{"dirty": "dirty", "committed": "unreachable-commits", "onbranch": "ahead-of-base"}
	for id, r := range want {
		if reasons[id] != r {
			t.Errorf("Kept[%s] = %q, want %q (all: %v)", id, reasons[id], r, reasons)
		}
	}
	for _, wt := range []worktree.Worktree{dirty, committed, onBranch} {
		if !exists(wt.Path) {
			t.Errorf("%s was deleted", wt.ID)
		}
	}
	if !hasBranch(t, repo, "w-onbranch") {
		t.Error("branch of kept worktree deleted")
	}
	if hasBranch(t, repo, "w-clean") {
		t.Error("branch of removed clean worktree should be deleted (merged)")
	}
}

func TestSweep_ActiveKeptOrphanReaped(t *testing.T) {
	m := mustNew(t, newRepo(t))
	a := mustCreate(t, m, worktree.Spec{ID: "active"})
	o := mustCreate(t, m, worktree.Spec{ID: "orphan"})
	rep, err := m.Sweep(ctx, worktree.OrphanedBy(map[string]bool{"active": true}), worktree.SweepOptions{})
	if err != nil || len(rep.Removed) != 1 || rep.Removed[0].ID != "orphan" {
		t.Fatalf("%+v, %v", rep, err)
	}
	if !exists(a.Path) || exists(o.Path) {
		t.Error("wrong worktree removed")
	}
}

// TestSweep_ProtectedNeverReaped is acceptance (g).
func TestSweep_ProtectedNeverReaped(t *testing.T) {
	m := mustNew(t, newRepo(t))
	p := mustCreate(t, m, worktree.Spec{ID: "prot"})
	q := mustCreate(t, m, worktree.Spec{ID: "other"})
	reapAll := worktree.Any(worktree.OrphanedBy(nil), worktree.TTL(1, func() time.Time { return time.Now().Add(240 * time.Hour) }))
	rep, err := m.Sweep(ctx, reapAll, worktree.SweepOptions{Protected: func(id string) bool { return id == "prot" }})
	if err != nil {
		t.Fatal(err)
	}
	if !exists(p.Path) || exists(q.Path) {
		t.Fatalf("protected=%v other=%v", exists(p.Path), exists(q.Path))
	}
	var found bool
	for _, k := range rep.Kept {
		found = found || (k.ID == "prot" && k.Reason == "protected")
	}
	if !found {
		t.Errorf("Kept = %+v, want prot/protected", rep.Kept)
	}
}

func TestSweep_TTL(t *testing.T) {
	m := mustNew(t, newRepo(t))
	old := mustCreate(t, m, worktree.Spec{ID: "old"})
	fresh := mustCreate(t, m, worktree.Spec{ID: "fresh"})
	dirtyOld := mustCreate(t, m, worktree.Spec{ID: "dirtyold"})
	write(t, filepath.Join(dirtyOld.Path, "wip"), "x")

	past := time.Now().Add(-72 * time.Hour)
	for _, wt := range []worktree.Worktree{old, dirtyOld} {
		if err := os.Chtimes(wt.Path, past, past); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := m.Sweep(ctx, worktree.TTL(24*time.Hour, nil), worktree.SweepOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Removed) != 1 || rep.Removed[0].ID != "old" {
		t.Fatalf("Removed = %+v", rep.Removed)
	}
	if !exists(fresh.Path) || !exists(dirtyOld.Path) {
		t.Fatal("fresh or dirty worktree removed")
	}
	// The preserved-work accumulation is surfaced, not hidden.
	var reason string
	for _, k := range rep.Kept {
		if k.ID == "dirtyold" {
			reason = k.Reason
		}
	}
	if reason != "dirty" {
		t.Errorf("dirtyold reason = %q, want dirty", reason)
	}
}

func TestSweep_ReportsUnregisteredLeftoversWithoutDeleting(t *testing.T) {
	repo := newRepo(t)
	m := mustNew(t, repo)
	leftover := m.RepoRoot() + "-worktrees-leftover"
	write(t, filepath.Join(leftover, "file"), "precious")
	gone := mustCreate(t, m, worktree.Spec{ID: "registered"})

	rep, err := m.Sweep(ctx, worktree.OrphanedBy(nil), worktree.SweepOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Unregistered) != 1 || !same(rep.Unregistered[0], leftover) {
		t.Errorf("Unregistered = %v, want [%s]", rep.Unregistered, leftover)
	}
	if !exists(filepath.Join(leftover, "file")) {
		t.Error("unregistered directory was touched")
	}
	if exists(gone.Path) {
		t.Error("registered clean worktree should have been swept")
	}
}

func TestSweep_PrunableReportedNotInspected(t *testing.T) {
	m := mustNew(t, newRepo(t))
	wt := mustCreate(t, m, worktree.Spec{ID: "pr"})
	if err := os.RemoveAll(wt.Path); err != nil {
		t.Fatal(err)
	}
	rep, err := m.Sweep(ctx, worktree.OrphanedBy(nil), worktree.SweepOptions{})
	if err != nil || len(rep.Kept) != 1 || rep.Kept[0].Reason != "prunable" {
		t.Fatalf("%+v, %v", rep, err)
	}
}

func TestSweep_Validation(t *testing.T) {
	m := mustNew(t, newRepo(t))
	if _, err := m.Sweep(ctx, nil, worktree.SweepOptions{}); err == nil {
		t.Error("nil policy accepted")
	}
	if _, err := m.Sweep(ctx, worktree.OrphanedBy(nil), worktree.SweepOptions{Branch: worktree.DeleteBranch}); !errors.Is(err, worktree.ErrForceRequired) {
		t.Errorf("DeleteBranch in sweep: err = %v", err)
	}
}

func TestAnyStrongestVerdictWins(t *testing.T) {
	keep := verdictPolicy(worktree.Keep)
	reap := verdictPolicy(worktree.Reap)
	shipped := verdictPolicy(worktree.ReapShipped)
	tests := []struct {
		p    worktree.SweepPolicy
		want worktree.Verdict
	}{
		{worktree.Any(), worktree.Keep},
		{worktree.Any(keep, keep), worktree.Keep},
		{worktree.Any(keep, reap), worktree.Reap},
		{worktree.Any(reap, shipped, keep), worktree.ReapShipped},
	}
	for i, tt := range tests {
		if got := tt.p.Verdict(ctx, worktree.Worktree{}, worktree.Status{}); got != tt.want {
			t.Errorf("case %d: %v, want %v", i, got, tt.want)
		}
	}
}

type verdictPolicy worktree.Verdict

func (v verdictPolicy) Verdict(context.Context, worktree.Worktree, worktree.Status) worktree.Verdict {
	return worktree.Verdict(v)
}

// TestConcurrentCreateRemove is acceptance (i); run it under -race.
func TestConcurrentCreateRemove(t *testing.T) {
	repo := newRepo(t)
	m := mustNew(t, repo)
	const n = 8

	var wg sync.WaitGroup
	wts := make([]worktree.Worktree, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wts[i], errs[i] = m.Create(ctx, worktree.Spec{ID: fmt.Sprintf("c%d", i)})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}
	if list, err := m.List(ctx); err != nil || len(list) != n {
		t.Fatalf("List = %d entries, %v; want %d", len(list), err, n)
	}

	// Same id from many goroutines: exactly one wins, the rest see ErrPathExists.
	var wins, exist int
	var mu sync.Mutex
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Create(ctx, worktree.Spec{ID: "same"})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, worktree.ErrPathExists):
				exist++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 || exist != n-1 {
		t.Fatalf("wins=%d exist=%d, want 1 and %d", wins, exist, n-1)
	}

	// Concurrent removal, mixed with a sweep and inspections.
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := m.Remove(ctx, wts[i], worktree.RemoveOptions{})
			if err != nil || !res.Removed {
				t.Errorf("Remove %d: %+v, %v", i, res, err)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := m.List(ctx); err != nil {
			t.Errorf("List: %v", err)
		}
	}()
	wg.Wait()
	list, _ := m.List(ctx)
	if len(list) != 1 || list[0].ID != "same" {
		t.Errorf("after removals List = %+v, want only 'same'", list)
	}
}

func TestExecRunnerErrorCarriesArgsAndStderr(t *testing.T) {
	_, err := worktree.ExecRunner().Run(ctx, t.TempDir(), "rev-parse", "--verify", "definitely-not-a-ref")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "rev-parse --verify definitely-not-a-ref") {
		t.Errorf("error lacks args: %v", err)
	}
	var ee interface{ ExitCode() int }
	if !errors.As(err, &ee) {
		t.Errorf("error does not wrap the exec error: %v", err)
	}
}

func TestExecRunnerIgnoresAmbientGitDir(t *testing.T) {
	repo := newRepo(t)
	other := newRepo(t)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	out, err := worktree.ExecRunner().Run(ctx, repo, "rev-parse", "--show-toplevel")
	if err != nil || !same(strings.TrimSpace(out), repo) {
		t.Errorf("out=%q err=%v; GIT_DIR leaked into the command", out, err)
	}
}

func TestDefaultBase_Fallbacks(t *testing.T) {
	run := worktree.ExecRunner()
	local := newRepo(t)
	if got, err := worktree.DefaultBase()(ctx, run, local); err != nil || got != "HEAD" {
		t.Errorf("no origin: %q, %v; want HEAD", got, err)
	}
	clone := newOriginClone(t)
	if got, err := worktree.DefaultBase()(ctx, run, clone); err != nil || got != "origin/main" {
		t.Errorf("with origin: %q, %v; want origin/main", got, err)
	}
	// Default branch is not main: fall back to origin/HEAD.
	git(t, clone, "update-ref", "refs/remotes/origin/trunk", "HEAD")
	git(t, clone, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
	git(t, clone, "update-ref", "-d", "refs/remotes/origin/main")
	if got, err := worktree.DefaultBase()(ctx, run, clone); err != nil || got != "origin/HEAD" {
		t.Errorf("origin/main missing: %q, %v; want origin/HEAD", got, err)
	}
	if _, err := worktree.FixedBase("nope")(ctx, run, local); err == nil {
		t.Error("FixedBase of a missing ref succeeded")
	}
	if got, err := worktree.LocalHead()(ctx, run, clone); err != nil || got != "HEAD" {
		t.Errorf("LocalHead: %q, %v", got, err)
	}
}

func TestWithFetch_RefreshesOriginBeforeCreate(t *testing.T) {
	work := newOriginClone(t)
	origin := git(t, work, "remote", "get-url", "origin")
	// Advance origin from a second clone.
	other := filepath.Join(t.TempDir(), "other")
	git(t, filepath.Dir(other), "clone", "-q", origin, other)
	newSHA := commitFile(t, other, "n.txt", "n")
	git(t, other, "push", "-q", "origin", "main")

	stale := mustCreate(t, mustNew(t, work), worktree.Spec{ID: "stale"})
	if stale.HEAD == newSHA {
		t.Fatal("test setup: expected a stale base without fetch")
	}
	fresh := mustCreate(t, mustNew(t, work, worktree.WithFetch()), worktree.Spec{ID: "fresh"})
	if fresh.HEAD != newSHA {
		t.Errorf("with WithFetch HEAD = %s, want %s", fresh.HEAD, newSHA)
	}
}

func TestWithFetch_NoOriginIsFine(t *testing.T) {
	m := mustNew(t, newRepo(t), worktree.WithFetch())
	mustCreate(t, m, worktree.Spec{ID: "x"})
}
