package worktree_test

import (
	"errors"
	"path/filepath"
	"testing"

	worktree "github.com/hollis-labs/libs/util/worktree"
)

// Translations of Nanite's internal/worktree/manager_test.go against the
// Nanite() preset, transcribed by reading that file, not by running Nanite.
// NoopManager is not part of this library, so TestNoopManager has no
// counterpart. Nanite's in-memory active map is gone: List comes from git,
// GetPath from the Placement.

func naniteMgr(t *testing.T) (*worktree.Manager, string, string) {
	t.Helper()
	repo := newRepo(t)
	base := filepath.Join(repo, ".nanite", "worktrees")
	return mustNew(t, repo, worktree.Nanite(base)...), repo, base
}

func TestNanitePreset_CreateAndRemove(t *testing.T) {
	m, repo, base := naniteMgr(t)
	wt := mustCreate(t, m, worktree.Spec{ID: "test-session-123"})

	if wt.Path != filepath.Join(base, "test-session-123") || wt.Branch != "worker-test-session-123" {
		t.Errorf("worktree = %+v", wt)
	}
	if !exists(wt.Path) || !hasBranch(t, repo, "worker-test-session-123") {
		t.Fatal("worktree directory or worker branch missing")
	}
	list, err := m.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "test-session-123" || list[0].Branch != "worker-test-session-123" {
		t.Fatalf("List = %+v, %v", list, err)
	}
	// GetPath equivalent: the Placement derives it without any stored state.
	if got := worktree.UnderRoot(base, "").Path(repo, "test-session-123"); got != wt.Path {
		t.Errorf("derived path %q != %q", got, wt.Path)
	}

	res, err := m.Remove(ctx, wt, worktree.RemoveOptions{})
	if err != nil || !res.Removed {
		t.Fatalf("Remove = %+v, %v", res, err)
	}
	if exists(wt.Path) {
		t.Error("worktree directory should be removed")
	}
	if list, _ := m.List(ctx); len(list) != 0 {
		t.Errorf("List after removal = %+v", list)
	}
	if hasBranch(t, repo, "worker-test-session-123") {
		t.Error("clean worker branch should be deleted (DeleteIfMerged)")
	}
}

// TestNanitePreset_CreateTwiceRefuses replaces TestCreateIdempotent. CHANGED:
// Nanite's manager returned the recorded path on a second Create; a stateless
// library reports ErrPathExists and the adopting adapter decides what to do.
func TestNanitePreset_CreateTwiceRefuses(t *testing.T) {
	m, _, _ := naniteMgr(t)
	first := mustCreate(t, m, worktree.Spec{ID: "sess-1"})
	_, err := m.Create(ctx, worktree.Spec{ID: "sess-1"})
	if !errors.Is(err, worktree.ErrPathExists) {
		t.Fatalf("err = %v, want ErrPathExists", err)
	}
	if !exists(first.Path) {
		t.Error("first worktree damaged by the refused Create")
	}
}

// TestNanitePreset_SweepOrphaned translates TestCleanupOrphaned.
func TestNanitePreset_SweepOrphaned(t *testing.T) {
	m, repo, _ := naniteMgr(t)
	active := mustCreate(t, m, worktree.Spec{ID: "active-session"})
	mustCreate(t, m, worktree.Spec{ID: "orphan-session"})
	if !hasBranch(t, repo, "worker-orphan-session") {
		t.Fatal("orphan branch should exist before the sweep")
	}

	rep, err := m.Sweep(ctx, worktree.OrphanedBy(map[string]bool{"active-session": true}), worktree.SweepOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Removed) != 1 || rep.Removed[0].ID != "orphan-session" {
		t.Fatalf("Removed = %+v, want the orphan only", rep.Removed)
	}
	list, _ := m.List(ctx)
	if len(list) != 1 || list[0].ID != "active-session" {
		t.Fatalf("List = %+v", list)
	}
	if hasBranch(t, repo, "worker-orphan-session") {
		t.Error("clean orphan's branch should be deleted after the sweep")
	}
	if !exists(active.Path) {
		t.Error("active worktree removed")
	}
}

func TestNanitePreset_RemoveNonexistent(t *testing.T) {
	m, repo, base := naniteMgr(t)
	res, err := m.Remove(ctx, worktree.Worktree{Path: filepath.Join(base, "nonexistent")}, worktree.RemoveOptions{})
	if err != nil || res.Removed || res.Reason != "absent" {
		t.Fatalf("%+v, %v", res, err)
	}
	if !exists(repo) {
		t.Fatal("repo damaged")
	}
}

// TestNanitePreset_StartupSweepNilKeepsWorkerWork is the regression test for
// the data-loss default: Nanite's startup CleanupOrphaned(nil) force-removed
// every worker worktree and its branch, dirty or not.
func TestNanitePreset_StartupSweepNilKeepsWorkerWork(t *testing.T) {
	m, repo, _ := naniteMgr(t)
	dirty := mustCreate(t, m, worktree.Spec{ID: "dirty"})
	write(t, filepath.Join(dirty.Path, "wip.go"), "package x")
	committed := mustCreate(t, m, worktree.Spec{ID: "committed"})
	sha := commitFile(t, committed.Path, "done.go", "package x")

	rep, err := m.Sweep(ctx, worktree.OrphanedBy(nil), worktree.SweepOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Removed) != 0 || len(rep.Kept) != 2 {
		t.Fatalf("Report = %+v", rep)
	}
	if !exists(filepath.Join(dirty.Path, "wip.go")) || !exists(committed.Path) {
		t.Error("worker files deleted")
	}
	if git(t, repo, "rev-parse", "worker-committed") != sha {
		t.Error("worker branch lost its commit")
	}
}

// TestNanitePreset_ForceIsTheCallersCall: a disposable worker can opt in to
// Nanite's old behavior explicitly.
func TestNanitePreset_ForceIsTheCallersCall(t *testing.T) {
	m, repo, _ := naniteMgr(t)
	wt := mustCreate(t, m, worktree.Spec{ID: "disposable"})
	write(t, filepath.Join(wt.Path, "junk"), "x")
	res, err := m.Remove(ctx, wt, worktree.RemoveOptions{Force: true, Branch: worktree.DeleteBranch})
	if err != nil || !res.Removed || !res.BranchDeleted {
		t.Fatalf("%+v, %v", res, err)
	}
	if exists(wt.Path) || hasBranch(t, repo, "worker-disposable") {
		t.Error("forced removal left something behind")
	}
}

func TestNanitePreset_RelativeBaseDirResolvesAgainstRepo(t *testing.T) {
	repo := newRepo(t)
	m := mustNew(t, repo, worktree.Nanite(".nanite/worktrees")...)
	wt := mustCreate(t, m, worktree.Spec{ID: "rel"})
	if !same(filepath.Dir(wt.Path), filepath.Join(repo, ".nanite", "worktrees")) {
		t.Errorf("Path = %q", wt.Path)
	}
}
