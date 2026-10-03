package worktree_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	worktree "github.com/hollis-labs/libs/util/worktree"
)

// These tests are translations of Torque's internal/worktree perrun_test.go
// and pr_cleanup_test.go, run against the Torque() preset. They were
// transcribed by reading those tests, not by running Torque; the assertions
// that changed are called out per test. Torque's run ids were int64 and are
// decimal strings here.

func torqueMgr(t *testing.T, repo, root string) *worktree.Manager {
	t.Helper()
	return mustNew(t, repo, worktree.Torque(worktree.TorqueOpts{Root: root})...)
}

func TestTorquePreset_CreatesCleanWorktreeAtOriginMain(t *testing.T) {
	repo := newOriginClone(t)
	root := filepath.Join(t.TempDir(), "wt-root")
	m := torqueMgr(t, repo, root)
	wt := mustCreate(t, m, worktree.Spec{ID: "42"})

	if wt.Path != filepath.Join(root, "run-42") {
		t.Errorf("Path = %q", wt.Path)
	}
	if head, want := git(t, wt.Path, "rev-parse", "HEAD"), git(t, repo, "rev-parse", "origin/main"); head != want {
		t.Errorf("HEAD = %s, want origin/main %s", head, want)
	}
}

func TestTorquePreset_DefaultRootIsTrueSibling(t *testing.T) {
	repo := newOriginClone(t)
	m := torqueMgr(t, repo, "")
	wt := mustCreate(t, m, worktree.Spec{ID: "7"})
	want := filepath.Join(filepath.Dir(m.RepoRoot()), filepath.Base(repo)+"-worktrees-run-7")
	if wt.Path != want {
		t.Errorf("Path = %q, want %q", wt.Path, want)
	}
	if filepath.Dir(wt.Path) != filepath.Dir(m.RepoRoot()) {
		t.Error("worktree must sit at the same directory depth as the repo")
	}
}

func TestTorquePreset_PathHelpers(t *testing.T) {
	if got := worktree.Sibling("run-").Path("/home/dev/myrepo", "42"); got != "/home/dev/myrepo-worktrees-run-42" {
		t.Errorf("sibling: %q", got)
	}
	if got := worktree.UnderRoot("/var/torque/wt", "run-").Path("/home/dev/myrepo", "9"); got != "/var/torque/wt/run-9" {
		t.Errorf("root override: %q", got)
	}
}

func TestTorquePreset_SiblingDepthPreservesRelativeReplace(t *testing.T) {
	repo := newOriginClone(t)
	write(t, filepath.Join(repo, "go.mod"), "module example.com/x\n\ngo 1.26\n\nreplace example.com/lib => ../lib\n")
	m := torqueMgr(t, repo, "")
	wt := mustCreate(t, m, worktree.Spec{ID: "5"})
	if filepath.Dir(wt.Path) != filepath.Dir(m.RepoRoot()) {
		t.Errorf("not a sibling: %s", wt.Path)
	}
}

func TestTorquePreset_BlocksRelativeReplaceAtWrongDepth(t *testing.T) {
	repo := newOriginClone(t)
	write(t, filepath.Join(repo, "go.mod"), "module example.com/x\n\ngo 1.26\n\nreplace example.com/lib => ../../lib\n")
	bad := filepath.Join(t.TempDir(), "elsewhere", "deeper")
	_, err := torqueMgr(t, repo, bad).Create(ctx, worktree.Spec{ID: "1"})
	if err == nil || !strings.Contains(err.Error(), "relative go.mod replace") {
		t.Fatalf("err = %v, want a relative-replace block", err)
	}
	if exists(filepath.Join(bad, "run-1")) {
		t.Error("worktree created despite the guard")
	}
}

func TestRelativeReplaceGuard(t *testing.T) {
	guard := worktree.RelativeReplaceGuard()
	check := func(repo, wt string) error { return guard(ctx, repo, wt) }

	if err := check(t.TempDir(), "/anywhere/run-1"); err != nil {
		t.Errorf("no go.mod: %v", err)
	}
	plain := t.TempDir()
	write(t, filepath.Join(plain, "go.mod"), "module m\n\ngo 1.26\n")
	if err := check(plain, "/anywhere/run-1"); err != nil {
		t.Errorf("no replace: %v", err)
	}
	abs := t.TempDir()
	write(t, filepath.Join(abs, "go.mod"), "module m\n\ngo 1.26\n\nreplace m/lib => /opt/lib\n")
	if err := check(abs, "/anywhere/run-1"); err != nil {
		t.Errorf("absolute replace: %v", err)
	}
	rel := filepath.Join(t.TempDir(), "repo")
	write(t, filepath.Join(rel, "go.mod"), "module m\n\ngo 1.26\n\nreplace m/lib => ../lib\n")
	if err := check(rel, worktree.Sibling("run-").Path(rel, "1")); err != nil {
		t.Errorf("relative replace + sibling: %v", err)
	}
	if err := check(rel, "/somewhere/else/run-1"); err == nil || !strings.Contains(err.Error(), "relative go.mod replace") {
		t.Errorf("relative replace + wrong depth: %v", err)
	}
	// Block form of replace.
	block := filepath.Join(t.TempDir(), "repo")
	write(t, filepath.Join(block, "go.mod"), "module m\n\nreplace (\n\tm/a => ./a\n)\n")
	if err := check(block, "/somewhere/else/run-1"); err == nil {
		t.Error("replace block with ./ target not detected")
	}
}

func TestTorquePreset_RejectsNonRepo(t *testing.T) {
	if _, err := worktree.New(t.TempDir(), worktree.Torque(worktree.TorqueOpts{})...); err == nil {
		t.Error("expected error for a non-repo")
	}
}

// TestTorquePreset_LocalRepoNoOrigin: a repo with no origin still gets a
// worktree, detached at local HEAD, without failing the fetch.
func TestTorquePreset_LocalRepoNoOrigin(t *testing.T) {
	repo := newRepo(t)
	wt := mustCreate(t, torqueMgr(t, repo, ""), worktree.Spec{ID: "1"})
	if git(t, wt.Path, "rev-parse", "HEAD") != git(t, repo, "rev-parse", "HEAD") {
		t.Error("worktree not at local HEAD")
	}
}

func TestTorquePreset_RemovesCleanWorktree(t *testing.T) {
	repo := newOriginClone(t)
	m := torqueMgr(t, repo, filepath.Join(t.TempDir(), "wt-root"))
	wt := mustCreate(t, m, worktree.Spec{ID: "1"})
	res, err := m.Remove(ctx, wt, worktree.RemoveOptions{})
	if err != nil || !res.Removed || exists(wt.Path) {
		t.Fatalf("%+v, %v", res, err)
	}
}

func TestTorquePreset_PreservesUncommittedWork(t *testing.T) {
	repo := newOriginClone(t)
	m := torqueMgr(t, repo, filepath.Join(t.TempDir(), "wt-root"))
	wt := mustCreate(t, m, worktree.Spec{ID: "1"})
	write(t, filepath.Join(wt.Path, "scratch.txt"), "wip")
	if res, err := m.Remove(ctx, wt, worktree.RemoveOptions{}); err != nil || res.Removed || !exists(wt.Path) {
		t.Fatalf("%+v, %v", res, err)
	}
}

func TestTorquePreset_PreservesCommitsAheadOfMain(t *testing.T) {
	repo := newOriginClone(t)
	m := torqueMgr(t, repo, filepath.Join(t.TempDir(), "wt-root"))
	wt := mustCreate(t, m, worktree.Spec{ID: "1"})
	git(t, wt.Path, "checkout", "-q", "-b", "task/CW-test")
	commitFile(t, wt.Path, "agent.go", "package x")
	if res, err := m.Remove(ctx, wt, worktree.RemoveOptions{}); err != nil || res.Removed || !exists(wt.Path) {
		t.Fatalf("%+v, %v", res, err)
	}
}

func TestTorquePreset_RemoveIdempotentWhenMissing(t *testing.T) {
	m := torqueMgr(t, newOriginClone(t), "")
	res, err := m.Remove(ctx, worktree.Worktree{Path: filepath.Join(t.TempDir(), "never-existed")}, worktree.RemoveOptions{})
	if err != nil || res.Removed {
		t.Fatalf("%+v, %v", res, err)
	}
}

// pr_cleanup translations ----------------------------------------------------

// TestTorquePreset_BranchNameOnDetachedAndBranch replaces WorktreeBranchName
// tests: detached reports "" and a branch reports the short name.
func TestTorquePreset_BranchNameOnDetachedAndBranch(t *testing.T) {
	repo := newOriginClone(t)
	m := torqueMgr(t, repo, filepath.Join(t.TempDir(), "wt-root"))
	wt := mustCreate(t, m, worktree.Spec{ID: "1"})
	if st, _ := m.Inspect(ctx, wt); st.Branch != "" {
		t.Errorf("detached Branch = %q", st.Branch)
	}
	git(t, wt.Path, "checkout", "-q", "-b", "fix/CW-20260519-0086-test")
	st, _ := m.Inspect(ctx, wt)
	list, _ := m.List(ctx)
	if st.Branch != "fix/CW-20260519-0086-test" || list[0].Branch != st.Branch {
		t.Errorf("Inspect branch %q, List branch %q", st.Branch, list[0].Branch)
	}
}

// TestTorquePreset_MergedSquashedWorktreeRemoved translates
// TestCleanupMergedPerRun_RemovesMergedSquashedWorktree. CHANGED: Torque
// matched by branch name alone; this needs the PR head commit (HeadOID).
func TestTorquePreset_MergedSquashedWorktreeRemoved(t *testing.T) {
	repo := newOriginClone(t)
	m := torqueMgr(t, repo, filepath.Join(t.TempDir(), "wt-root"))
	wt := mustCreate(t, m, worktree.Spec{ID: "42"})
	git(t, wt.Path, "checkout", "-q", "-b", "fix/CW-20260519-0086-test")
	sha := commitFile(t, wt.Path, "code.go", "package x")

	if res, _ := m.Remove(ctx, wt, worktree.RemoveOptions{}); res.Removed {
		t.Fatal("plain removal must preserve this worktree")
	}
	rep := sweepMerged(t, m, fakeSource{prs: []worktree.MergedRef{{HeadRef: "fix/CW-20260519-0086-test", HeadOID: sha}}})
	if len(rep.Removed) != 1 || exists(wt.Path) {
		t.Fatalf("merged worktree not removed: %+v", rep)
	}
	if hasBranch(t, repo, "fix/CW-20260519-0086-test") {
		t.Error("local branch must be deleted after a merged-PR reap")
	}
}

func TestTorquePreset_MergedPreservesUnmergedWorktree(t *testing.T) {
	repo := newOriginClone(t)
	m := torqueMgr(t, repo, filepath.Join(t.TempDir(), "wt-root"))
	wt := mustCreate(t, m, worktree.Spec{ID: "1"})
	git(t, wt.Path, "checkout", "-q", "-b", "fix/CW-test-open")
	sha := commitFile(t, wt.Path, "code.go", "package x")
	rep := sweepMerged(t, m, fakeSource{prs: []worktree.MergedRef{{HeadRef: "some/other-branch", HeadOID: sha}}})
	if len(rep.Removed) != 0 || !exists(wt.Path) {
		t.Fatalf("%+v", rep)
	}
}

// TestTorquePreset_NilMergedSetCollapsesToPlainCleanup translates
// TestCleanupMergedPerRun_NilSetCollapsesToPlainCleanup with Any(orphan, merged).
func TestTorquePreset_NilMergedSetCollapsesToPlainCleanup(t *testing.T) {
	repo := newOriginClone(t)
	m := torqueMgr(t, repo, filepath.Join(t.TempDir(), "wt-root"))
	clean := mustCreate(t, m, worktree.Spec{ID: "1"})
	ahead := mustCreate(t, m, worktree.Spec{ID: "2"})
	git(t, ahead.Path, "checkout", "-q", "-b", "feat/CW-x")
	commitFile(t, ahead.Path, "f.go", "package x")

	rep, err := m.Sweep(ctx, worktree.Any(worktree.OrphanedBy(nil), worktree.MergedPR(nil)), worktree.SweepOptions{})
	if err != nil || len(rep.Errs) != 0 {
		t.Fatalf("%+v, %v", rep, err)
	}
	if exists(clean.Path) || !exists(ahead.Path) {
		t.Errorf("clean exists=%v ahead exists=%v; want clean gone, ahead kept", exists(clean.Path), exists(ahead.Path))
	}
}

// TestTorquePreset_SweepMergedRemovesMergedOnly translates
// TestSweepMergedPerRun_ForceRemovesMergedWorktreesOnly (minus the force).
func TestTorquePreset_SweepMergedRemovesMergedOnly(t *testing.T) {
	repo := newOriginClone(t)
	m := torqueMgr(t, repo, filepath.Join(t.TempDir(), "wt-root"))
	merged := mustCreate(t, m, worktree.Spec{ID: "100"})
	git(t, merged.Path, "checkout", "-q", "-b", "fix/CW-merged")
	sha := commitFile(t, merged.Path, "a.go", "package x")
	open := mustCreate(t, m, worktree.Spec{ID: "101"})
	git(t, open.Path, "checkout", "-q", "-b", "fix/CW-open")
	commitFile(t, open.Path, "b.go", "package x")
	detached := mustCreate(t, m, worktree.Spec{ID: "102"})

	rep := sweepMerged(t, m, fakeSource{prs: []worktree.MergedRef{{HeadRef: "fix/CW-merged", HeadOID: sha}}})
	if len(rep.Errs) != 0 || len(rep.Removed) != 1 || rep.Removed[0].Path != merged.Path {
		t.Fatalf("%+v", rep)
	}
	if exists(merged.Path) || !exists(open.Path) || !exists(detached.Path) {
		t.Error("wrong set of worktrees survived")
	}
}

func TestTorquePreset_SweepMergedEmptySetIsNoop(t *testing.T) {
	repo := newOriginClone(t)
	m := torqueMgr(t, repo, filepath.Join(t.TempDir(), "wt-root"))
	wt := mustCreate(t, m, worktree.Spec{ID: "1"})
	git(t, wt.Path, "checkout", "-q", "-b", "fix/CW-x")
	if rep := sweepMerged(t, m, fakeSource{}); len(rep.Removed) != 0 || len(rep.Errs) != 0 || !exists(wt.Path) {
		t.Fatalf("%+v", rep)
	}
}

// TestTorquePreset_OldCleanWorktreeSwept covers SweepPerRun's TTL path: a
// clean worktree older than the cutoff goes, a dirty one is kept.
func TestTorquePreset_OldCleanWorktreeSwept(t *testing.T) {
	repo := newOriginClone(t)
	m := torqueMgr(t, repo, "")
	old := mustCreate(t, m, worktree.Spec{ID: "1"})
	fresh := mustCreate(t, m, worktree.Spec{ID: "2"})
	past := timeAgo(72)
	if err := os.Chtimes(old.Path, past, past); err != nil {
		t.Fatal(err)
	}
	rep, err := m.Sweep(ctx, worktree.TTL(24*time.Hour, nil), worktree.SweepOptions{})
	if err != nil || len(rep.Removed) != 1 || exists(old.Path) || !exists(fresh.Path) {
		t.Fatalf("%+v, %v", rep, err)
	}
}
