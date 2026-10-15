package worktree

// TorqueOpts configures the [Torque] preset.
type TorqueOpts struct {
	// Root, when set, places worktrees at <Root>/run-<id> instead of beside
	// the repository. A Root at a different depth than the repository is
	// refused by the relative-replace guard when the repository's go.mod has
	// relative replaces.
	Root string
}

// Torque returns the options reproducing Torque's per-run worktrees: detached
// checkouts at origin/main (falling back to origin/HEAD, then HEAD) after a
// best-effort fetch, placed beside the repository as
// <repoName>-worktrees-run-<id> (or <Root>/run-<id>), with the
// relative-replace guard on. Callers format their run number as the id.
func Torque(o TorqueOpts) []Option {
	pl := Sibling("run-")
	if o.Root != "" {
		pl = UnderRoot(o.Root, "run-")
	}
	return []Option{
		WithPlacement(pl),
		WithBaseRef(DefaultBase()),
		WithFetch(),
		WithGuard(RelativeReplaceGuard()),
	}
}

// Nanite returns the options reproducing Nanite's worker worktrees: each
// session gets <baseDir>/<id> on a new branch worker-<id> cut from the
// repository's HEAD. baseDir may be relative to the repository root. Unlike
// Nanite's original manager, removal preserves work; see the package
// documentation.
func Nanite(baseDir string) []Option {
	return []Option{
		WithPlacement(UnderRoot(baseDir, "")),
		WithBaseRef(LocalHead()),
		WithBranchNamer(func(id string) string { return "worker-" + id }),
	}
}

// Tether returns the options reproducing Tether's per-run work roots:
// <workspaceRoot>/<id>/repo cut from the repository's HEAD, detached unless
// the caller passes [Spec.Branch]. Tether's rule that only a literal name
// becomes a branch (templates and path-like names stay detached) is
// application logic and stays in Tether.
func Tether(workspaceRoot string) []Option {
	return []Option{
		WithPlacement(Nested(workspaceRoot, "repo")),
		WithBaseRef(LocalHead()),
	}
}
