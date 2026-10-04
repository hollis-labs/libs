package workspace

import (
	"cmp"
	"slices"
)

type LockKey struct{ Namespace, CanonicalID string }

// OrderedLockKeys builds the complete deduplicated order from supplied physical
// observations. It neither resolves paths nor acquires locks. The namespace
// must be protected by the host and outside all mutation roots.
func OrderedLockKeys(namespace string, roots []RootRef, observed []RootObservation) ([]LockKey, error) {
	if !cleanAbsolute(namespace) {
		return nil, refuse("unsafe_lock_namespace", "locks", Conflict)
	}
	byID := map[string]RootObservation{}
	for _, o := range observed {
		if _, dup := byID[o.RootID]; dup {
			return nil, refuse("duplicate_root_observation", "roots", Conflict)
		}
		byID[o.RootID] = o
	}
	keys := []LockKey{}
	seen := map[string]RootRef{}
	refsByID := map[string]RootRef{}
	for _, r := range roots {
		if err := r.Validate(); err != nil {
			return nil, err
		}
		if prior, ok := refsByID[r.ID]; ok && prior != r {
			return nil, refuse("ambiguous_root_reference", "roots", Conflict)
		}
		refsByID[r.ID] = r
		o, ok := byID[r.ID]
		if !ok || o.Uncertainty != "" || !cleanAbsolute(o.CanonicalPath) || !cleanAbsolute(o.CanonicalBase) || !within(o.CanonicalBase, o.CanonicalPath) {
			return nil, refuse("unknown_canonical_root", "roots", Unsupported)
		}
		if o.Owner != r.Owner {
			return nil, refuse("root_owner_mismatch", "roots", Conflict)
		}
		if within(o.CanonicalPath, namespace) || within(namespace, o.CanonicalPath) {
			return nil, refuse("lock_namespace_inside_root", "locks", Conflict)
		}
		if prior, ok := seen[o.CanonicalPath]; ok {
			if prior.ID != r.ID || prior.Owner != r.Owner {
				return nil, refuse("ambiguous_root_alias", "roots", Conflict)
			}
			continue
		}
		for canonical := range seen {
			if within(canonical, o.CanonicalPath) || within(o.CanonicalPath, canonical) {
				return nil, refuse("overlapping_mutation_roots", "roots", Conflict)
			}
		}
		seen[o.CanonicalPath] = r
		keys = append(keys, LockKey{namespace, o.CanonicalPath})
	}
	slices.SortFunc(keys, func(a, b LockKey) int {
		if n := cmp.Compare(a.Namespace, b.Namespace); n != 0 {
			return n
		}
		return cmp.Compare(a.CanonicalID, b.CanonicalID)
	})
	return keys, nil
}
