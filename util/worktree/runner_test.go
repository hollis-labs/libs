package worktree_test

import (
	"context"
	"slices"
	"sync"
	"testing"

	worktree "github.com/hollis-labs/libs/util/worktree"
)

type recorder struct {
	mu    sync.Mutex
	calls [][]string
}

func (r *recorder) Run(ctx context.Context, dir string, args ...string) (string, error) {
	r.mu.Lock()
	r.calls = append(r.calls, slices.Clone(args))
	r.mu.Unlock()
	return worktree.ExecRunner().Run(ctx, dir, args...)
}

func (r *recorder) has(sub ...string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if len(c) >= len(sub) && slices.Equal(c[:len(sub)], sub) {
			return true
		}
	}
	return false
}

// TestNoNetworkByDefault: the default base policy and Create never run fetch;
// WithFetch is the only thing that does, and only when origin exists.
func TestNoNetworkByDefault(t *testing.T) {
	work := newOriginClone(t)
	rec := &recorder{}
	m := mustNew(t, work, worktree.WithRunner(rec))
	wt := mustCreate(t, m, worktree.Spec{ID: "n"})
	if _, err := m.Inspect(ctx, wt); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Remove(ctx, wt, worktree.RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if rec.has("fetch") || rec.has("pull") || rec.has("ls-remote") {
		t.Errorf("network command issued by default: %v", rec.calls)
	}

	rec2 := &recorder{}
	mustCreate(t, mustNew(t, work, worktree.WithRunner(rec2), worktree.WithFetch()), worktree.Spec{ID: "f"})
	if !rec2.has("fetch", "origin") {
		t.Errorf("WithFetch did not fetch: %v", rec2.calls)
	}
}

// TestRemoveNeverDoubleForces: git needs -f -f to remove a locked worktree;
// this library must never pass two force flags.
func TestRemoveNeverDoubleForces(t *testing.T) {
	repo := newRepo(t)
	rec := &recorder{}
	m := mustNew(t, repo, worktree.WithRunner(rec))
	wt := mustCreate(t, m, worktree.Spec{ID: "df"})
	git(t, repo, "worktree", "lock", wt.Path)
	if _, err := m.Remove(ctx, wt, worktree.RemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, c := range rec.calls {
		if len(c) > 1 && c[0] == "worktree" && c[1] == "remove" {
			t.Errorf("git worktree remove issued for a locked worktree: %v", c)
		}
	}
}

func TestWithRunnerNilIsIgnored(t *testing.T) {
	m := mustNew(t, newRepo(t), worktree.WithRunner(nil), worktree.WithPlacement(nil), worktree.WithBaseRef(nil), worktree.WithGuard(nil))
	mustCreate(t, m, worktree.Spec{ID: "ok"})
}
