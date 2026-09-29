# go-worktree

Create, inspect, safely remove and sweep per-run git worktrees with injected policy.

It manages the `git worktree` lifecycle for tools that give each agent run, worker session or job its own checkout. Where the checkout goes, which commit it starts from, what the branch is called and what a sweep may reap are all supplied by the caller. What is not negotiable is the default: **removal preserves work**.

## Status

**Pre-release.** This project is unreleased, not deployed, and has no outside consumers. It's being built in the open: the code, the docs, and this README describe what exists today, not a pitch for what's planned. Interfaces and behavior change without notice, and there are no compatibility guarantees yet.

## Install

```sh
go get github.com/hollis-labs/go-worktree
```

Requires Go 1.26.6 or newer and a `git` binary on `PATH` at runtime. The root package is standard library only. The `ghmerged` sub-package also needs the GitHub CLI (`gh`) at runtime, and only when you use it.

## Usage

```go
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
```

The same program lives in [`examples/hello`](./examples/hello/main.go); run it with `go run ./examples/hello`.

## What it does

A `Manager` is bound to one explicit repository root (it never reads the process working directory) and holds no per-worktree state. A worktree is a caller-chosen `id`; a `Placement` maps it to a path and back, so a path can always be re-derived from `(repo, id)` without being stored. `List` and `Sweep` act only on worktrees that `git worktree list --porcelain` reports.

| Piece | Choices |
|---|---|
| Placement | `Sibling(prefix)` beside the repo at the same depth, `UnderRoot(root, prefix)`, `Nested(root, leaf)` |
| Base ref | `DefaultBase()` (`origin/main`, `origin/HEAD`, `HEAD`; no network), `FixedBase(ref)`, `LocalHead()`; `WithFetch()` opts into `git fetch origin` |
| Branch | detached by default; `Spec.Branch` or `WithBranchNamer` for a new branch |
| Guards | `RelativeReplaceGuard()` refuses placements that break relative `go.mod` replaces (opt-in, Go-specific) |
| Sweep policy | `TTL`, `OrphanedBy`, `MergedPR` (evidence-based), `Any`; your own `SweepPolicy` |
| Presets | `Torque`, `Nanite`, `Tether` reproduce the three placements this was extracted from |

### The safety default

`Remove` and `Sweep` keep a worktree, and say why in `RemoveResult.Reason` or `Report.Kept`, when it is:

- **dirty**: modified, staged or untracked files;
- holding **unreachable commits**: commits on HEAD that no branch, remote-tracking ref or tag reaches (`git rev-list --count HEAD --not --branches --remotes --tags`), which removal would orphan. This check works in a repository with no `origin`;
- **ahead of base**: commits the base ref does not contain;
- **locked**, even with `Force` (the library never passes git's double `--force`);
- **not inspectable**: any git failure while checking means keep.

`RemoveOptions.Force` skips the first three checks and the inspection-failure check, and nothing else. The branch is deleted with `git branch -d` by default, which git refuses for an unmerged branch, so a removed worktree's commits survive on its branch. `DeleteBranch` needs `Force` or shipped-work proof.

`Sweep` never forces. A policy that wants a worktree gone still cannot remove one holding work; the worktree shows up in `Report.Kept`. Nothing here expires preserved work: that is an application decision. Directories that match the placement but are not registered with git are listed in `Report.Unregistered` and never deleted.

`MergedPR` reaps a worktree only on evidence that its work shipped: its branch matches a merged PR's head ref, it is not dirty or locked, and its HEAD is equal to or an ancestor of that PR's head commit, which must exist locally. A name match alone is not enough (`TrustBranchName()` opts into that). The `ghmerged` sub-package supplies the PR list from `gh pr list --state merged --json headRefName,headRefOid`.

## Provenance: what was verified, what was transcribed

Lifted from three applications' worktree code (Torque `internal/worktree`, Nanite `internal/worktree`, Tether `internal/workspace/workroot.go`).

- **Transcribed from reading**, not verified against a run of those apps: the placements, the base-ref chain, the relative-replace guard, the collision message and prune hints, the `worker-<id>` naming, and every test under `preset_*_test.go`, which are translations of the apps' tests. This library is not claimed to behave identically to any of them; the deliberate differences are listed in [CHANGELOG.md](./CHANGELOG.md).
- **Verified by tests in this repository**, against real `git` (developed with 2.47.0 on macOS) in temporary repositories: the safety default above, including the no-origin case that Torque's guard mishandled, locked and dirty worktrees, `OrphanedBy(nil)`, evidence-based merged reaping, id validation, concurrent `Create`/`Remove` under `-race`, and the porcelain parser. `gh` is exercised only through a shell-script stand-in, never a real `gh`.

## Known limitations

- Only git 2.47.0 on macOS has been exercised locally. Linux runs through CI once the repository is pushed. Windows is not supported.
- The mutex serializes mutations through one `Manager` in one process. Two processes are serialized only by git's own locking, and there is a gap between `Inspect` and the removal that follows: git itself still refuses to remove a dirty worktree, but a commit made in that window is not caught.
- Ignored files are deleted silently along with the worktree (git behavior; not detected).
- Submodules and stashes are not handled. Stashes are shared by the repository and survive removal.
- `git worktree list --porcelain` output is parsed line by line; a worktree path containing a newline is not supported.
- Removing a worktree whose directory is already gone runs `git worktree prune`, which drops every stale registration in the repository, not just that one.
- `Status.AheadOfBase` is computed against the manager's base policy, not a per-`Spec.BaseRef` override. When the base cannot be resolved it is -1 and does not block removal (the unreachable-commit check still does).
- A detached worktree can never match a merged PR (there is no branch name to match), and `MergedPR` cannot reap when the PR's head commit is not in the local object store.
- `Create` is not idempotent: a second `Create` for the same id returns `ErrPathExists`.
- The relative-replace check is a permissive regular expression over `go.mod` text.
- `DeleteMergedBranches` (Torque's branch-only sweep) is not implemented.

## Compatibility

This module is pre-1.0 and unreleased: minor versions may break the exported API, and until a first release there is nothing to be compatible with. Once tagged, pin an exact version and read [CHANGELOG.md](./CHANGELOG.md) before upgrading. The `go` directive is 1.26.6, the floor for every hollis-labs module.

## Out of scope

- Environment-variable parsing and app-level mode switches (`TORQUE_WORKTREE_*`, shared versus worktree mode).
- Nanite's `Manager` interface and `NoopManager`, Tether's `launch.Plan`, and Torque's branch-name prefix lists.
- Scanning for and deleting directories git does not list; `os.RemoveAll` of anything.
- Running `gh` from the root package (only `ghmerged` does).
- Submodule and stash handling, Windows, and expiring preserved work.
- Worktrees of bare repositories.

## Development

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

Tests create their own temporary repositories and set `GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_NOSYSTEM=1` and a private `HOME`; they never touch your repositories or git config. CI (`.github/workflows/check.yml`) is the full gate.

## License

MIT — see [LICENSE](./LICENSE).
