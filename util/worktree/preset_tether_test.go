package worktree_test

import (
	"path/filepath"
	"strings"
	"testing"

	worktree "github.com/hollis-labs/libs/util/worktree"
)

// Translations of Tether's internal/workspace/workroot_test.go against the
// Tether() preset, transcribed by reading that file, not by running Tether.
// Not ported because they test Tether's own logic, which stays in Tether:
// the shared/hybrid workspace modes, launch.Plan handling, and the rule that
// only a literal name becomes a branch (path-like and template names stay
// detached).

func tetherMgr(t *testing.T) (*worktree.Manager, string, string) {
	t.Helper()
	repo := newRepo(t)
	root := t.TempDir()
	return mustNew(t, repo, worktree.Tether(root)...), repo, root
}

// TestTetherPreset_IsolatedWorktree translates
// TestMaterializeWorkRootCreatesIsolatedGitWorktree. CHANGED: the agent left an
// untracked file behind; Tether removed it with an unconditional --force. Here
// the default keeps it, and Force (the adopting caller's decision) removes it.
func TestTetherPreset_IsolatedWorktree(t *testing.T) {
	m, repo, root := tetherMgr(t)
	wt := mustCreate(t, m, worktree.Spec{ID: "session-123"})

	want := filepath.Join(root, "session-123", "repo")
	if wt.Path != want {
		t.Fatalf("Path = %q, want %q", wt.Path, want)
	}
	if !exists(filepath.Join(want, ".git")) {
		t.Fatal("worktree .git missing")
	}
	write(t, filepath.Join(want, "agent.txt"), "agent edit\n")
	if exists(filepath.Join(repo, "agent.txt")) {
		t.Fatal("agent edit leaked into source repo")
	}
	if out := git(t, repo, "branch", "--list", "tether/*"); out != "" {
		t.Fatalf("worktree launch created local tether branch: %s", out)
	}

	if res, err := m.Remove(ctx, wt, worktree.RemoveOptions{}); err != nil || res.Removed || res.Reason != "dirty" {
		t.Fatalf("default Remove = %+v, %v; want kept as dirty", res, err)
	}
	if res, err := m.Remove(ctx, wt, worktree.RemoveOptions{Force: true}); err != nil || !res.Removed {
		t.Fatalf("forced Remove = %+v, %v", res, err)
	}
	if exists(want) {
		t.Fatal("worktree dir still exists after cleanup")
	}
}

func TestTetherPreset_CollisionSurfacesClearError(t *testing.T) {
	m, _, _ := tetherMgr(t)
	mustCreate(t, m, worktree.Spec{ID: "session-collide"})
	wt, err := m.Create(ctx, worktree.Spec{ID: "session-collide"})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want an actionable already-exists error", err)
	}
	if wt.Path != "" {
		t.Errorf("colliding Create returned a worktree: %+v", wt)
	}
}

func TestTetherPreset_RegisteredPathSurfacesClearError(t *testing.T) {
	m, _, _ := tetherMgr(t)
	wt := mustCreate(t, m, worktree.Spec{ID: "session-reg"})
	// Delete the directory but leave the registration intact.
	if err := removeAll(wt.Path); err != nil {
		t.Fatal(err)
	}
	_, err := m.Create(ctx, worktree.Spec{ID: "session-reg"})
	if err == nil || !strings.Contains(err.Error(), "worktree") {
		t.Fatalf("err = %v, want a framed error", err)
	}
}

func TestTetherPreset_NamedBranch(t *testing.T) {
	m, repo, _ := tetherMgr(t)
	wt := mustCreate(t, m, worktree.Spec{ID: "session-named", Branch: "tether-session-abc"})
	if !strings.Contains(git(t, repo, "branch", "--list", "tether-session-abc"), "tether-session-abc") {
		t.Fatal("named worktree did not create its branch")
	}
	if res, err := m.Remove(ctx, wt, worktree.RemoveOptions{}); err != nil || !res.Removed {
		t.Fatalf("%+v, %v", res, err)
	}
}

func TestTetherPreset_NoBranchMeansDetached(t *testing.T) {
	m, repo, _ := tetherMgr(t)
	wt := mustCreate(t, m, worktree.Spec{ID: "session-detached"})
	if wt.Branch != "" || git(t, wt.Path, "rev-parse", "--abbrev-ref", "HEAD") != "HEAD" {
		t.Errorf("expected detached: %+v", wt)
	}
	if strings.Count(git(t, repo, "branch", "--list"), "\n") != 0 {
		t.Errorf("extra branches: %s", git(t, repo, "branch", "--list"))
	}
}

// TestTetherPreset_RemoveDeregisters translates
// TestRemoveWorktreeAtDeregistersRegistration.
func TestTetherPreset_RemoveDeregisters(t *testing.T) {
	m, repo, _ := tetherMgr(t)
	wt := mustCreate(t, m, worktree.Spec{ID: "session-dereg"})
	if res, err := m.Remove(ctx, wt, worktree.RemoveOptions{}); err != nil || !res.Removed {
		t.Fatalf("%+v, %v", res, err)
	}
	if out := git(t, repo, "worktree", "list"); strings.Contains(out, "session-dereg") {
		t.Fatalf("worktree still registered: %q", out)
	}
	// Idempotent: removing an already-gone worktree is not an error.
	if res, err := m.Remove(ctx, wt, worktree.RemoveOptions{}); err != nil || res.Removed || res.Reason != "absent" {
		t.Fatalf("second Remove = %+v, %v", res, err)
	}
}

// TestTetherPreset_PruneSeesWhatIsAtRisk: the `mux workspaces prune`
// call site was blind to dirty work; Inspect is what it should call.
func TestTetherPreset_PruneSeesWhatIsAtRisk(t *testing.T) {
	m, _, _ := tetherMgr(t)
	wt := mustCreate(t, m, worktree.Spec{ID: "session-work"})
	commitFile(t, wt.Path, "out.txt", "result")
	list, err := m.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("%+v, %v", list, err)
	}
	st, err := m.Inspect(ctx, list[0])
	if err != nil || st.UnreachableCommits != 1 {
		t.Fatalf("Inspect = %+v, %v", st, err)
	}
}
