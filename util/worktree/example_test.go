package worktree_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	worktree "github.com/hollis-labs/go-worktree"
)

// exampleRepo creates a throwaway repository with one commit. Examples print
// only path base names, so output does not depend on the temp location.
func exampleRepo() (repo string, cleanup func()) {
	tmp, err := os.MkdirTemp("", "go-worktree-example-")
	if err != nil {
		panic(err)
	}
	repo = filepath.Join(tmp, "app")
	if err := os.Mkdir(repo, 0o750); err != nil {
		panic(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...) //nolint:gosec // fixed arguments in an example
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			panic(fmt.Sprintf("git %v: %v\n%s", args, err, out))
		}
	}
	return repo, func() { os.RemoveAll(tmp) }
}

func ExampleNew() {
	repo, cleanup := exampleRepo()
	defer cleanup()
	ctx := context.Background()

	m, err := worktree.New(repo) // default: Sibling(""), DefaultBase(), detached checkouts
	if err != nil {
		panic(err)
	}
	wt, err := m.Create(ctx, worktree.Spec{ID: "run-1"})
	if err != nil {
		panic(err)
	}
	fmt.Println(filepath.Base(wt.Path), wt.Branch == "")

	res, err := m.Remove(ctx, wt, worktree.RemoveOptions{})
	fmt.Println(res.Removed, err)
	// Output:
	// app-worktrees-run-1 true
	// true <nil>
}

func ExampleManager_Remove() {
	repo, cleanup := exampleRepo()
	defer cleanup()
	ctx := context.Background()
	m, _ := worktree.New(repo)
	wt, _ := m.Create(ctx, worktree.Spec{ID: "work"})

	// A commit on a detached HEAD is reachable from no branch: removing the
	// worktree would orphan it, so Remove refuses.
	cmd := exec.Command("git", "commit", "-q", "--allow-empty", "-m", "agent work")
	cmd.Dir = wt.Path
	if out, err := cmd.CombinedOutput(); err != nil {
		panic(fmt.Sprintf("%v\n%s", err, out))
	}
	res, _ := m.Remove(ctx, wt, worktree.RemoveOptions{})
	fmt.Println(res.Removed, res.Reason, res.Status.UnreachableCommits)
	// Output: false unreachable-commits 1
}

func ExampleManager_Inspect() {
	repo, cleanup := exampleRepo()
	defer cleanup()
	ctx := context.Background()
	m, _ := worktree.New(repo)
	wt, _ := m.Create(ctx, worktree.Spec{ID: "look"})

	if err := os.WriteFile(filepath.Join(wt.Path, "scratch.txt"), []byte("wip"), 0o600); err != nil {
		panic(err)
	}
	st, err := m.Inspect(ctx, wt)
	fmt.Println(st.Dirty, st.UnreachableCommits, st.AheadOfBase, err)
	// Output: true 0 0 <nil>
}

func ExampleManager_List() {
	repo, cleanup := exampleRepo()
	defer cleanup()
	ctx := context.Background()
	m, _ := worktree.New(repo, worktree.WithBranchNamer(func(id string) string { return "job/" + id }))
	for _, id := range []string{"b", "a"} {
		if _, err := m.Create(ctx, worktree.Spec{ID: id}); err != nil {
			panic(err)
		}
	}
	list, _ := m.List(ctx)
	for _, wt := range list {
		fmt.Println(wt.ID, wt.Branch)
	}
	// Output:
	// a job/a
	// b job/b
}

func ExampleManager_Sweep() {
	repo, cleanup := exampleRepo()
	defer cleanup()
	ctx := context.Background()
	m, _ := worktree.New(repo)
	for _, id := range []string{"live", "finished"} {
		if _, err := m.Create(ctx, worktree.Spec{ID: id}); err != nil {
			panic(err)
		}
	}

	// Everything not in the active set is reaped, subject to the safety checks.
	rep, err := m.Sweep(ctx, worktree.OrphanedBy(map[string]bool{"live": true}), worktree.SweepOptions{})
	fmt.Println(len(rep.Removed), rep.Removed[0].ID, rep.Kept[0].ID, rep.Kept[0].Reason, err)
	// Output: 1 finished live policy <nil>
}

func ExampleSibling() {
	p := worktree.Sibling("run-")
	path := p.Path("/home/dev/myrepo", "42")
	id, ok := p.ID("/home/dev/myrepo", path)
	fmt.Println(path, id, ok)
	// Output: /home/dev/myrepo-worktrees-run-42 42 true
}

func ExampleUnderRoot() {
	p := worktree.UnderRoot("/var/work", "job-")
	fmt.Println(p.Path("/home/dev/myrepo", "7"))
	// Output: /var/work/job-7
}

func ExampleNested() {
	p := worktree.Nested("/var/sessions", "repo")
	fmt.Println(p.Path("/home/dev/myrepo", "s1"))
	// Output: /var/sessions/s1/repo
}

func ExampleDefaultBase() {
	repo, cleanup := exampleRepo()
	defer cleanup()
	// No origin remote: the chain falls back to the local HEAD, offline.
	ref, err := worktree.DefaultBase()(context.Background(), worktree.ExecRunner(), repo)
	fmt.Println(ref, err)
	// Output: HEAD <nil>
}

func ExampleFixedBase() {
	repo, cleanup := exampleRepo()
	defer cleanup()
	_, err := worktree.FixedBase("origin/release")(context.Background(), worktree.ExecRunner(), repo)
	fmt.Println(err != nil)
	// Output: true
}

func ExampleLocalHead() {
	repo, cleanup := exampleRepo()
	defer cleanup()
	ref, _ := worktree.LocalHead()(context.Background(), worktree.ExecRunner(), repo)
	fmt.Println(ref)
	// Output: HEAD
}

func ExampleFindRepoRoot() {
	repo, cleanup := exampleRepo()
	defer cleanup()
	sub := filepath.Join(repo, "internal", "pkg")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		panic(err)
	}
	root, err := worktree.FindRepoRoot(sub)
	fmt.Println(filepath.Base(root), err)
	// Output: app <nil>
}

func ExampleRelativeReplaceGuard() {
	repo, cleanup := exampleRepo()
	defer cleanup()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module m\n\nreplace m/lib => ../lib\n"), 0o600); err != nil {
		panic(err)
	}
	m, _ := worktree.New(repo,
		worktree.WithPlacement(worktree.UnderRoot(filepath.Join(repo, "deeper"), "")),
		worktree.WithGuard(worktree.RelativeReplaceGuard()))
	_, err := m.Create(context.Background(), worktree.Spec{ID: "x"})
	fmt.Println(err != nil)
	// Output: true
}

func ExampleTTL() {
	repo, cleanup := exampleRepo()
	defer cleanup()
	ctx := context.Background()
	m, _ := worktree.New(repo)
	if _, err := m.Create(ctx, worktree.Spec{ID: "old"}); err != nil {
		panic(err)
	}
	// Pretend a day and a half has passed.
	later := func() time.Time { return time.Now().Add(36 * time.Hour) }
	rep, _ := m.Sweep(ctx, worktree.TTL(24*time.Hour, later), worktree.SweepOptions{})
	fmt.Println(len(rep.Removed))
	// Output: 1
}

func ExampleOrphanedBy() {
	p := worktree.OrphanedBy(map[string]bool{"a": true})
	fmt.Println(p.Verdict(context.Background(), worktree.Worktree{ID: "a"}, worktree.Status{}) == worktree.Keep,
		p.Verdict(context.Background(), worktree.Worktree{ID: "b"}, worktree.Status{}) == worktree.Reap)
	// Output: true true
}

func ExampleAny() {
	p := worktree.Any(worktree.OrphanedBy(map[string]bool{"a": true}), worktree.TTL(time.Hour, nil))
	fmt.Println(p.Verdict(context.Background(), worktree.Worktree{ID: "b"}, worktree.Status{}) == worktree.Reap)
	// Output: true
}

type prs []worktree.MergedRef

func (p prs) Merged(context.Context, string) ([]worktree.MergedRef, error) { return p, nil }

func ExampleMergedPR() {
	repo, cleanup := exampleRepo()
	defer cleanup()
	ctx := context.Background()
	m, _ := worktree.New(repo)
	wt, _ := m.Create(ctx, worktree.Spec{ID: "pr", Branch: "fix/a"})

	// A merged PR for fix/a whose head is exactly this worktree's HEAD is
	// proof the work shipped; a name match alone would not be.
	src := prs{{HeadRef: "fix/a", HeadOID: wt.HEAD}}
	rep, _ := m.Sweep(ctx, worktree.MergedPR(src), worktree.SweepOptions{})
	fmt.Println(len(rep.Removed))
	// Output: 1
}

func ExampleTorque() {
	repo, cleanup := exampleRepo()
	defer cleanup()
	m, _ := worktree.New(repo, worktree.Torque(worktree.TorqueOpts{})...)
	wt, _ := m.Create(context.Background(), worktree.Spec{ID: "42"})
	fmt.Println(filepath.Base(wt.Path))
	// Output: app-worktrees-run-42
}

func ExampleNanite() {
	repo, cleanup := exampleRepo()
	defer cleanup()
	m, _ := worktree.New(repo, worktree.Nanite(".nanite/worktrees")...)
	wt, _ := m.Create(context.Background(), worktree.Spec{ID: "sess1"})
	fmt.Println(wt.Branch, filepath.Base(wt.Path))
	// Output: worker-sess1 sess1
}

func ExampleTether() {
	repo, cleanup := exampleRepo()
	defer cleanup()
	root, _ := os.MkdirTemp("", "go-worktree-ws-")
	defer os.RemoveAll(root)
	m, _ := worktree.New(repo, worktree.Tether(root)...)
	wt, _ := m.Create(context.Background(), worktree.Spec{ID: "sess1"})
	fmt.Println(filepath.Base(filepath.Dir(wt.Path)), filepath.Base(wt.Path))
	// Output: sess1 repo
}

func ExampleExecRunner() {
	out, err := worktree.ExecRunner().Run(context.Background(), os.TempDir(), "--version")
	fmt.Println(len(out) > 0, err)
	// Output: true <nil>
}

func ExampleWithFetch() {
	repo, cleanup := exampleRepo()
	defer cleanup()
	// No origin remote: the fetch is skipped and creation proceeds.
	m, _ := worktree.New(repo, worktree.WithFetch())
	_, err := m.Create(context.Background(), worktree.Spec{ID: "f"})
	fmt.Println(err)
	// Output: <nil>
}

type countingRunner struct{ n *int }

func (c countingRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	*c.n++
	return worktree.ExecRunner().Run(ctx, dir, args...)
}

func ExampleWithRunner() {
	repo, cleanup := exampleRepo()
	defer cleanup()
	var calls int
	m, _ := worktree.New(repo, worktree.WithRunner(countingRunner{&calls}))
	_, _ = m.List(context.Background())
	fmt.Println(calls > 0)
	// Output: true
}

func ExampleWithPlacement() {
	repo, cleanup := exampleRepo()
	defer cleanup()
	root, _ := os.MkdirTemp("", "go-worktree-root-")
	defer os.RemoveAll(root)
	m, _ := worktree.New(repo, worktree.WithPlacement(worktree.UnderRoot(root, "job-")))
	wt, _ := m.Create(context.Background(), worktree.Spec{ID: "1"})
	fmt.Println(filepath.Base(wt.Path))
	// Output: job-1
}

func ExampleWithBaseRef() {
	repo, cleanup := exampleRepo()
	defer cleanup()
	m, _ := worktree.New(repo, worktree.WithBaseRef(worktree.FixedBase("main")))
	wt, err := m.Create(context.Background(), worktree.Spec{ID: "1"})
	fmt.Println(wt.HEAD != "", err)
	// Output: true <nil>
}

func ExampleWithBranchNamer() {
	repo, cleanup := exampleRepo()
	defer cleanup()
	m, _ := worktree.New(repo, worktree.WithBranchNamer(func(id string) string { return "agent/" + id }))
	wt, _ := m.Create(context.Background(), worktree.Spec{ID: "9"})
	fmt.Println(wt.Branch)
	// Output: agent/9
}

func ExampleWithGuard() {
	repo, cleanup := exampleRepo()
	defer cleanup()
	deny := func(context.Context, string, string) error { return fmt.Errorf("not today") }
	m, _ := worktree.New(repo, worktree.WithGuard(deny))
	_, err := m.Create(context.Background(), worktree.Spec{ID: "1"})
	fmt.Println(err)
	// Output: not today
}

func ExampleTrustBranchName() {
	repo, cleanup := exampleRepo()
	defer cleanup()
	ctx := context.Background()
	m, _ := worktree.New(repo)
	_, _ = m.Create(ctx, worktree.Spec{ID: "t", Branch: "fix/a"})

	// No commit id in the source, so only a name match is available. Without
	// TrustBranchName nothing is reaped; with it, Torque's old behavior.
	src := prs{{HeadRef: "fix/a"}}
	rep, _ := m.Sweep(ctx, worktree.MergedPR(src), worktree.SweepOptions{})
	fmt.Println(len(rep.Removed))
	rep, _ = m.Sweep(ctx, worktree.MergedPR(src, worktree.TrustBranchName()), worktree.SweepOptions{})
	fmt.Println(len(rep.Removed))
	// Output:
	// 0
	// 1
}
