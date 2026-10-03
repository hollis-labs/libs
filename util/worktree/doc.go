// Package worktree creates, inspects, safely removes and sweeps per-run git
// worktrees, with every policy (placement, base ref, branch naming, guards,
// sweep rules) injected by the caller.
//
// The package is stateless: there is no in-memory registry of active
// worktrees. A worktree is identified by a caller-chosen id that a [Placement]
// maps to a path and back, so an application can always re-derive the path
// from (repository, id) without persisting it. [Manager.List] and
// [Manager.Sweep] act only on worktrees that git itself reports through
// `git worktree list --porcelain`.
//
// # Safety default
//
// Removal preserves work. [Manager.Remove] and [Manager.Sweep] keep any
// worktree that is dirty (modified or untracked files), holds commits that no
// branch, remote-tracking ref or tag can reach ([Status.UnreachableCommits]),
// is ahead of the base ref ([Status.AheadOfBase]), is locked, or cannot be
// inspected. Only an explicit [RemoveOptions.Force] overrides the first three
// checks, and a locked worktree is never removed (the library never passes
// the double `--force` git requires for that). Ignored files are deleted
// silently by git with the worktree; that is not detected.
//
// # Shape
//
//   - [New] binds a [Manager] to one explicit repository root and never reads
//     the process working directory.
//   - [Sibling], [UnderRoot] and [Nested] are the built-in placements.
//   - [DefaultBase], [FixedBase] and [LocalHead] choose the commit a new
//     worktree starts from. None of them touch the network; [WithFetch] opts
//     into a best-effort `git fetch origin` before creation.
//   - [SweepPolicy] values ([TTL], [OrphanedBy], [MergedPR], [Any]) decide
//     which worktrees a sweep tries to reap. The manager's safety checks still
//     apply to whatever a policy asks for.
//   - [Torque], [Nanite] and [Tether] are option presets that reproduce the
//     three placements this package was extracted from. They double as
//     conformance fixtures in the test suite.
//
// The sub-package ghmerged provides a `gh`-backed [MergedSource]; the root
// package never runs `gh`.
package worktree
