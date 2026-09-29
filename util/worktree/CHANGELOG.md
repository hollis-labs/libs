# Changelog

All notable changes to go-worktree are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## Unreleased

### Added

- Package `worktree` (standard library only): `Manager` (`New`, `Create`,
  `Inspect`, `List`, `Remove`, `Sweep`), `FindRepoRoot`, `Runner`/`ExecRunner`,
  placements (`Sibling`, `UnderRoot`, `Nested`), base refs (`DefaultBase`,
  `FixedBase`, `LocalHead`), `RelativeReplaceGuard`, sweep policies (`TTL`,
  `OrphanedBy`, `MergedPR`, `Any`) and the `Torque`, `Nanite` and `Tether`
  option presets.
- Package `ghmerged`: `gh`-backed `MergedSource`.
- Lifted from Torque `internal/worktree`, Nanite `internal/worktree` and Tether
  `internal/workspace/workroot.go`, transcribed from reading those files, not
  verified against a run of those applications.

### Changed from the applications this was lifted from

Two tightenings are intended; the rest follow from the library being stateless
and callers-supply-policy.

- Tightened: a worktree holding commits that no branch, remote-tracking ref or
  tag reaches is kept (`Status.UnreachableCommits`). Torque's guard treated a
  failed `rev-list origin/main..HEAD` as "no work", so in a repository with no
  origin a clean detached worktree with commits was removed and the commits
  orphaned.
- Tightened: merged-PR reaping needs evidence, not a branch name. The worktree's
  HEAD must be equal to or an ancestor of the PR's head commit (present
  locally), and the worktree must be clean and unlocked. Torque force-removed by
  `headRefName` alone and deleted the branch with `-D`. `TrustBranchName()`
  restores the name match but still refuses dirty and locked worktrees.
- Removal never forces by default. Nanite's `Cleanup`/`CleanupOrphaned` (`--force`,
  `os.RemoveAll` fallback, `branch -D`, everything under the base directory an
  orphan when the active set is nil) and Tether's always-`--force` removal become
  an explicit `RemoveOptions.Force`. `OrphanedBy(nil)` reaps only worktrees
  holding no work.
- `Create` refuses an existing path (`ErrPathExists`) instead of returning the
  recorded path (Nanite's idempotent `Create`); there is no in-memory registry.
- `Sweep` acts only on worktrees git lists; unregistered leftovers are reported
  in `Report.Unregistered`, never deleted.
- A failing or missing `gh` is an error in `Report.Errs` instead of an empty
  result.
- `New` takes an explicit repository root; the process working directory is never
  consulted. Every git call carries a `context.Context`.
- Not carried over: environment parsing, `NoopManager`, Torque's branch-only
  `SweepMergedBranches` (deferred), `Config.KeepDays`.
