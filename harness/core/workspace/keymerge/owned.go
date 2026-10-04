package keymerge

// ownedSet is the owned key paths as a trie, so a walk down a desired document
// can ask at each key whether that exact path is owned, and move to the owned
// paths beneath it, without building path strings.
//
// Every method accepts a nil receiver: a nil set owns nothing.
type ownedSet struct {
	next map[string]*ownedSet
	leaf bool
}

// newOwnedSet builds the set. A path with no components cannot name a key and
// is refused. Duplicate paths are one path. The paths are copied; the caller's
// slices are never retained.
func newOwnedSet(paths []KeyPath) (*ownedSet, error) {
	root := &ownedSet{}
	for _, path := range paths {
		if len(path) == 0 {
			return nil, &Error{Code: CodeInvalidOwnedPaths, Reason: ReasonEmptyPath}
		}
		node := root
		for _, part := range path {
			if node.next == nil {
				node.next = make(map[string]*ownedSet)
			}
			child := node.next[part]
			if child == nil {
				child = &ownedSet{}
				node.next[part] = child
			}
			node = child
		}
		node.leaf = true
	}
	return root, nil
}

// child returns the owned paths beneath key name, or nil when there are none.
func (s *ownedSet) child(name string) *ownedSet {
	if s == nil {
		return nil
	}
	return s.next[name]
}

// owns reports whether the path this set stands for is itself an owned path.
func (s *ownedSet) owns() bool { return s != nil && s.leaf }

// errNotLeaf is the refusal for an owned path that names a non-empty object or
// table in the desired document: owned paths name leaves.
func errNotLeaf() error {
	return &Error{Code: CodeInvalidOwnedPaths, Reason: ReasonNotLeaf}
}
