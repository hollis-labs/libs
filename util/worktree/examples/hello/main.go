package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"

	worktree "github.com/hollis-labs/go-worktree"
)

func main() {
	// A throwaway repository, so the example touches nothing of yours.
	dir, err := os.MkdirTemp("", "go-worktree-demo-")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=demo", "-c", "user.email=demo@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...) //nolint:gosec // fixed arguments
		cmd.Dir = dir
		if out, runErr := cmd.CombinedOutput(); runErr != nil {
			log.Fatalf("git %v: %v\n%s", args, runErr, out)
		}
	}

	ctx := context.Background()
	mgr, err := worktree.New(dir) // explicit repo root; default placement is a sibling directory
	if err != nil {
		log.Fatal(err)
	}
	wt, err := mgr.Create(ctx, worktree.Spec{ID: "run-1"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("created at the repo's sibling:", wt.Path != "" && wt.Path != mgr.RepoRoot())

	res, err := mgr.Remove(ctx, wt, worktree.RemoveOptions{})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("removed:", res.Removed) // clean and nothing unreachable, so it goes
}
