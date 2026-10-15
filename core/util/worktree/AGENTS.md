# go-worktree

Create, inspect, safely remove and sweep per-run git worktrees with injected policy.

It is not a scheduler, an agent launcher or an app-config layer: it does git worktree lifecycle only, and every policy is injected. Do not add environment parsing, `gh` in the root package, or `os.RemoveAll` of anything.

## Start Here

- `worktree` package (module root) — the importable API; `doc.go` is the package documentation. `manager.go` (Create/Inspect/Remove), `sweep.go`, `merged.go`, `placement.go`, `base.go`, `guard.go`, `presets.go`, `porcelain.go`, `runner.go`.
- `ghmerged` — the only place that runs `gh`.
- `examples/hello/main.go` — the runnable example; the README `## Usage` fence must stay identical to it.
- `.github/workflows/check.yml` — the full CI gate; `release.yml` refuses a tag with no CHANGELOG heading.

## Commands

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## Boundaries

- No `replace` directive in `go.mod` and no committed `go.work`: consumers cannot resolve either.
- Standard library only, no hollis libs: `go list -deps ./...` shows nothing outside the stdlib and this module.
- Removal preserves work. Dirty, unreachable-commit, ahead-of-base, locked and uninspectable worktrees are kept: `TestRemove_DirtyKept`, `TestRemove_NoOriginDetachedCommitKept`, `TestRemove_LockedKept`, `TestRemove_InspectFailureKeeps`, `TestSweep_OrphanedByNilKeepsWork`. Never add a code path that force-removes without `RemoveOptions.Force`.
- Never pass two `--force` flags to `git worktree remove` (`TestRemoveNeverDoubleForces`). Locked means kept, even with Force.
- `UnreachableCommits` uses `--not --branches --remotes --tags`. `--not --all` includes the worktree's own HEAD and always returns 0.
- Merged-PR reaping needs commit-id evidence (`TestMergedPR_RefusesWhenHeadNotAncestor`). Do not fall back to a name match by default.
- No network by default (`TestNoNetworkByDefault`); `WithFetch` is the only fetch.
- git reports symlink-resolved paths (macOS `/private/var`). Compare paths through `canon`; `Worktree.Path` is spelled as the Placement spells it (`TestPlacementRoundTripThroughSymlinks`).
- Tests must stay hermetic: `TestMain` in `helpers_test.go` sets `GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_NOSYSTEM=1` and a private `HOME`. Tests create their own temporary repositories; never point one at a real repository.
- Behavioral equivalence with Torque, Nanite or Tether is NOT established. The `preset_*_test.go` files are translations from reading the apps' tests, not captures from runs. Do not describe the lib as identical to them.
- Out of scope: env parsing, submodules, stashes, Windows, scanning/deleting unregistered directories, expiring preserved work, `DeleteMergedBranches` (deferred).
