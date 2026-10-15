package worktree

import (
	"context"
	"fmt"
)

// BaseRef chooses the ref a new worktree starts from. It is called with the
// manager's Runner and the repository root, and returns a ref name that git
// can resolve to a commit. It must not use the network.
type BaseRef func(ctx context.Context, r Runner, repoRoot string) (string, error)

// DefaultBase returns the first of origin/main, origin/HEAD and HEAD that
// resolves to a commit. It never fetches, so it degrades the freshness of the
// checkout (a repository with no origin, an offline clone, a default branch
// that is not main) and never the ability to create one.
func DefaultBase() BaseRef {
	return firstResolving("origin/main", "origin/HEAD", "HEAD")
}

// FixedBase returns ref if it resolves to a commit and an error otherwise.
func FixedBase(ref string) BaseRef { return firstResolving(ref) }

// LocalHead returns "HEAD" of the repository root, the base used by Nanite
// and Tether.
func LocalHead() BaseRef { return firstResolving("HEAD") }

func firstResolving(refs ...string) BaseRef {
	return func(ctx context.Context, r Runner, repoRoot string) (string, error) {
		for _, ref := range refs {
			if _, err := resolveCommit(ctx, r, repoRoot, ref); err == nil {
				return ref, nil
			}
		}
		return "", fmt.Errorf("no base ref resolved among %v", refs)
	}
}

// resolveCommit returns the commit id ref points to, or an error.
func resolveCommit(ctx context.Context, r Runner, dir, ref string) (string, error) {
	if ref == "" || ref[0] == '-' {
		return "", fmt.Errorf("invalid ref %q", ref)
	}
	out, err := r.Run(ctx, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	return trimNL(out), nil
}
