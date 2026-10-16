package nativefiles

import "slices"

// Document carries encoded native bytes and the declared leaf-key ownership
// needed by an installed-layer apply. Object containers are traversed, arrays
// and scalars are owned as values, and empty objects declare no leaf keys.
// It contains no existing-document merge or I/O behavior.
type Document struct {
	Bytes    []byte
	KeyPaths [][]string
}

// ObjectKeyPaths returns detached, deterministic leaf paths from an already
// validated native object. Components remain separate so dots in keys are data.
func ObjectKeyPaths(doc map[string]any) [][]string {
	out := [][]string{}
	var walk func([]string, map[string]any)
	walk = func(prefix []string, object map[string]any) {
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			parts := append(slices.Clone(prefix), key)
			if child, ok := object[key].(map[string]any); ok {
				walk(parts, child)
			} else {
				out = append(out, parts)
			}
		}
	}
	walk(nil, doc)
	return out
}
