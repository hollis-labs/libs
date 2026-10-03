package worktree

import "errors"

var (
	// ErrPathExists is returned by [Manager.Create] when the target path is
	// already present on disk.
	ErrPathExists = errors.New("worktree path already exists")

	// ErrInvalidID is returned when a worktree id is empty or not path-safe.
	ErrInvalidID = errors.New("invalid worktree id")

	// ErrNotRepo is returned by [New] and [FindRepoRoot] when the given
	// directory is not the root of a git working tree.
	ErrNotRepo = errors.New("not a git repository root")

	// ErrForceRequired is returned when [DeleteBranch] is requested without
	// [RemoveOptions.Force] and without shipped-work proof.
	ErrForceRequired = errors.New("deleting an unmerged branch requires Force")
)
