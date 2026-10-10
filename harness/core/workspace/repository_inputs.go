package workspace

import (
	"cmp"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"github.com/hollis-labs/substrate/harness/workspace/repositories"
)

// repositoryInputs validates explicit host-resolved inputs, without resolving a
// template or discovering Git state. Fresh leaf preflight runs under all locks.
func repositoryInputs(p PlannedWorkspace, obs map[string]RootObservation) error {
	in := p.spec.EffectInputs.Repositories
	if len(in) != len(p.spec.Repos) {
		return refuse("repository_input_binding", "repositories", Unsupported)
	}
	seen := map[int]bool{}
	paths := map[string]bool{}
	for _, r := range in {
		if r.Header != (effects.Header{}) || r.CandidateRootID != p.spec.Boot.Candidate.ID || r.SourceIdentity == "" || r.CommonIdentity == "" || paths[r.Path] {
			return refuse("repository_input_binding", "repositories", Conflict)
		}
		paths[r.Path] = true
		for _, root := range []effects.RootInput{r.Source, r.Common, r.Base} {
			declared, ok := findRoot(p.roots, root.ID)
			o := obs[root.ID]
			if !ok || root.Path != o.CanonicalPath || root.AllowedBase != o.CanonicalBase || root.Owner != declared.Owner || root.Provenance != declared.Provenance || root.MutationIdentity != o.CanonicalPath || !o.Exists || !o.Directory {
				return refuse("repository_input_binding", "repositories", Conflict)
			}
		}
		if filepath.Dir(r.Path) != r.Base.Path {
			return refuse("repository_input_binding", "repositories", Conflict)
		}
		matched := -1
		for n, s := range p.spec.Repos {
			if s.ID == r.RepositoryID && string(s.Mode) == string(r.Mode) && s.Source.ID == r.Source.ID && s.Source.Path == obs[r.Source.ID].DeclaredPath && s.Source.Provenance == r.Source.Provenance && obs[s.DesiredRoot.ID].CanonicalPath == r.Path && s.BaseCommit == r.BaseCommit && (s.Existing != nil) == r.Existing && s.Clone == r.Clone.Requirement {
				matched = n
			}
		}
		if matched < 0 || seen[matched] {
			return refuse("repository_input_binding", "repositories", Conflict)
		}
		seen[matched] = true
		semantic := p.spec.Repos[matched]
		if semantic.BranchTemplate != "" && !strings.Contains(semantic.BranchTemplate, "${") && semantic.BranchTemplate != r.Branch {
			return refuse("repository_input_binding", "repositories", Conflict)
		}
		grant := EffectGrant{Kind: RepositoryEffect, RootID: r.Base.ID, AuthorizationID: r.AuthorizationID, Version: r.AuthorizationVersion}
		if !slices.Contains(p.spec.Effects, grant) || !slices.Contains(p.resources.Grants, grant) {
			return refuse("repository_input_binding", "repositories", Conflict)
		}
		if !r.Existing {
			if r.Mode == repositories.Checkout {
				return refuse("private_checkout_unsupported", "repositories", Unsupported)
			}
			if r.Mode != repositories.Worktree && r.Mode != repositories.Clone || r.Ownership != repositories.Owned || !r.Base.PrivateCustody || semantic.DesiredRoot.Owner != r.Base.Owner {
				return refuse("repository_input_binding", "repositories", Conflict)
			}
			// A clone input is frozen: selection happens under locks at apply.
			// Its worktree fallback needs its own explicit grant on the shared
			// Git metadata root, never the clone's grant.
			if r.Mode == repositories.Clone {
				fallback := EffectGrant{Kind: RepositoryEffect, RootID: r.Common.ID, AuthorizationID: r.Clone.FallbackAuthorizationID, Version: r.Clone.FallbackAuthorizationVersion}
				if r.Clone.Method != "" || r.Clone.FallbackReason != "" || r.Clone.FallbackAuthorizationID != "" && (!slices.Contains(p.spec.Effects, fallback) || !slices.Contains(p.resources.Grants, fallback)) {
					return refuse("repository_input_binding", "repositories", Conflict)
				}
			} else if r.Clone != (repositories.CloneIntent{}) {
				return refuse("repository_input_binding", "repositories", Conflict)
			}
		} else {
			if r.Ownership != repositories.UserOwned {
				return refuse("repository_input_binding", "repositories", Conflict)
			}
			if r.Mode == repositories.Checkout {
				write := EffectGrant{Kind: RepositoryEffect, RootID: semantic.DesiredRoot.ID, AuthorizationID: r.UserWriteAuthorizationID, Version: r.UserWriteAuthorizationVersion}
				if r.UserWriteAuthorizationID == "" || r.UserWriteAuthorizationVersion == "" || !slices.Contains(p.spec.Effects, write) || !slices.Contains(p.resources.Grants, write) {
					return refuse("repository_input_binding", "repositories", Conflict)
				}
			} else if r.Mode != repositories.Readonly || !r.Readonly.Enforced || r.Readonly.Path != r.Path || r.Readonly.CommonPath != r.Common.Path || r.Readonly.ID == "" || r.Readonly.Revision == "" || r.Readonly.Provenance == "" {
				return refuse("repository_input_binding", "repositories", Conflict)
			}
		}
	}
	return nil
}

// Overlapping Git source/common roots need separate exact canonical keys. Keep
// the outer mutation-root overlap guard intact, then validate each additional
// root against the declared external namespace and union before acquisition.
func repositoryLockKeys(p PlannedWorkspace) ([]LockKey, error) {
	keys := slices.Clone(p.locks)
	for _, r := range p.spec.EffectInputs.Repositories {
		for _, input := range []effects.RootInput{r.Source, r.Common, r.Base} {
			root, ok := findRoot(p.roots, input.ID)
			if !ok {
				return nil, refuse("repository_input_binding", "repositories", Conflict)
			}
			extra, err := OrderedLockKeys(p.resources.LockNamespace, []RootRef{root}, p.observed.Roots)
			if err != nil {
				return nil, err
			}
			keys = append(keys, extra...)
		}
	}
	slices.SortFunc(keys, func(a, b LockKey) int {
		if n := cmp.Compare(a.Namespace, b.Namespace); n != 0 {
			return n
		}
		return cmp.Compare(a.CanonicalID, b.CanonicalID)
	})
	return slices.Compact(keys), nil
}
