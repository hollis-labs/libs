package workspace

import (
	"cmp"
	"path/filepath"
	"slices"
)

type LockKey struct{ Namespace, CanonicalID string }

// OrderedLockKeys uses observations bound to declared paths for both resources
// and the protected lock namespace. Alias spellings resolve to the same keys.
// It performs no filesystem operations and grants no ownership or lock proof.
func OrderedLockKeys(namespace string, roots []RootRef, observed []RootObservation) ([]LockKey, error) {
	if !cleanAbsolute(namespace) {
		return nil, refuse(CodeUnsafeLockNamespace, "locks", Conflict)
	}
	if err := validateFrozenValues(roots, observed); err != nil {
		return nil, err
	}
	byID := map[string]RootObservation{}
	canonicalNamespace := ""
	for _, o := range observed {
		if _, dup := byID[o.RootID]; dup {
			return nil, refuse(CodeDuplicateRootObservation, "roots", Conflict)
		}
		byID[o.RootID] = o
		if o.DeclaredPath == namespace {
			if o.Uncertainty != "" || !o.Exists || !o.Directory || !cleanAbsolute(o.CanonicalPath) || !cleanAbsolute(o.CanonicalBase) || !within(o.CanonicalBase, o.CanonicalPath) || o.CanonicalPath == o.CanonicalBase {
				return nil, refuse(CodeUnknownLockNamespace, "locks", Unsupported)
			}
			if canonicalNamespace != "" && canonicalNamespace != o.CanonicalPath {
				return nil, refuse(CodeAmbiguousLockNamespace, "locks", Conflict)
			}
			canonicalNamespace = o.CanonicalPath
		}
	}
	if canonicalNamespace == "" {
		return nil, refuse(CodeUnknownLockNamespace, "locks", Unsupported)
	}
	paths := map[string]RootRef{}
	refs := map[string]RootRef{}
	for _, r := range roots {
		if err := r.Validate(); err != nil {
			return nil, err
		}
		if prior, ok := refs[r.ID]; ok && prior != r {
			return nil, refuse(CodeAmbiguousRootReference, "roots", Conflict)
		}
		refs[r.ID] = r
		o, ok := byID[r.ID]
		if !ok || o.Uncertainty != "" || !cleanAbsolute(o.CanonicalPath) || !cleanAbsolute(o.CanonicalBase) || !within(o.CanonicalBase, o.CanonicalPath) {
			return nil, refuse(CodeUnknownCanonicalRoot, "roots", Unsupported)
		}
		if o.DeclaredPath != r.Path {
			return nil, refuse(CodeObservationRootMismatch, "roots", Conflict)
		}
		if o.Owner != r.Owner {
			return nil, refuse(CodeRootOwnerMismatch, "roots", Conflict)
		}
		if within(o.CanonicalPath, canonicalNamespace) || within(canonicalNamespace, o.CanonicalPath) {
			return nil, refuse(CodeLockNamespaceInsideRoot, "locks", Conflict)
		}
		if prior, ok := paths[o.CanonicalPath]; ok && prior != r {
			return nil, refuse(CodeAmbiguousRootAlias, "roots", Conflict)
		}
		paths[o.CanonicalPath] = r
	}
	keys := make([]LockKey, 0, len(paths))
	for canonical := range paths {
		for parent := filepath.Dir(canonical); parent != filepath.Dir(parent); parent = filepath.Dir(parent) {
			if _, exists := paths[parent]; exists {
				return nil, refuse(CodeOverlappingMutationRoots, "roots", Conflict)
			}
		}
		keys = append(keys, LockKey{canonicalNamespace, canonical})
	}
	slices.SortFunc(keys, func(a, b LockKey) int {
		if n := cmp.Compare(a.Namespace, b.Namespace); n != 0 {
			return n
		}
		return cmp.Compare(a.CanonicalID, b.CanonicalID)
	})
	return keys, nil
}
