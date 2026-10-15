package hooks

import "fmt"

// Resolve merges the three registration layers into one set. It is a pure
// function: no I/O, no globals, no clock, and it never mutates its inputs.
//
// Precedence is whole-hook, by Name: the highest-precedence layer that
// defines a given Name wins outright, and the other definitions of that name
// are discarded, not merged field by field. The result lists managed hooks
// first, then surviving user hooks, then surviving project hooks, each in
// input order, with Layer set on every element.
//
// A duplicate Name inside a single layer, or an empty Name, is an error.
// Resolve does not validate hooks; call Hook.Validate on every element of the
// result.
func Resolve(managed, user, project []Hook) ([]Hook, error) {
	layers := []struct {
		layer Layer
		hooks []Hook
	}{
		{LayerManaged, managed},
		{LayerUser, user},
		{LayerProject, project},
	}
	seen := make(map[string]Layer, len(managed)+len(user)+len(project))
	out := make([]Hook, 0, len(managed)+len(user)+len(project))
	for _, l := range layers {
		inLayer := make(map[string]struct{}, len(l.hooks))
		for _, h := range l.hooks {
			if h.Name == "" {
				return nil, fmt.Errorf("hooks: %s layer has a hook with an empty Name", l.layer)
			}
			if _, dup := inLayer[h.Name]; dup {
				return nil, fmt.Errorf("hooks: %s layer defines %q more than once", l.layer, h.Name)
			}
			inLayer[h.Name] = struct{}{}
			if _, shadowed := seen[h.Name]; shadowed {
				continue
			}
			seen[h.Name] = l.layer
			h.Layer = l.layer
			h.CommandArgs = append([]string(nil), h.CommandArgs...)
			if len(h.CommandArgs) == 0 {
				h.CommandArgs = nil
			}
			out = append(out, h)
		}
	}
	return out, nil
}
