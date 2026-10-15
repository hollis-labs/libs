package launch

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	contracts "github.com/hollis-labs/substrate/llm-core/contracts"

	"github.com/hollis-labs/substrate/agent/toolselect/profile"
)

// noTools is an allow entry no real tool name matches. Tool names are never
// empty, and a Profile with an empty ToolsAllow allows everything, so an
// intersection that leaves nothing must not be expressed as an empty list.
const noTools = ""

// FromAssignment derives the per-launch Profile from base, the launch
// profile's named base Profile, narrowed by a.Grants.MCP. See the package
// documentation for the exact rules.
//
// The result is never wider than base: every tool it shows, base shows. A
// malformed glob in the grant is profile.ErrBadPattern (wrapped). Assignment
// validity is the host's concern; FromAssignment does not call a.Validate.
func FromAssignment(base profile.Profile, a contracts.Assignment) (profile.Profile, error) {
	grant := a.Grants.MCP
	for _, list := range [][]string{grant.Allow, grant.Deny} {
		for _, g := range list {
			if _, err := path.Match(g, ""); err != nil {
				return profile.Profile{}, fmt.Errorf("%w: grants.mcp entry %q", profile.ErrBadPattern, g)
			}
		}
	}

	out := base
	out.Servers = maps.Clone(base.Servers)
	out.Order = slices.Clone(base.Order)
	out.AlwaysLoad = slices.Clone(base.AlwaysLoad)
	out.ToolsDeny = union(base.ToolsDeny, grant.Deny)
	out.ToolsAllow = slices.Clone(base.ToolsAllow)

	switch {
	case len(grant.Allow) == 0:
		// No allow ceiling.
	case len(base.ToolsAllow) == 0:
		out.ToolsAllow = union(grant.Allow)
	default:
		out.ToolsAllow = intersect(base.ToolsAllow, grant.Allow)
	}
	return out, nil
}

// union returns the distinct entries of the lists in first-seen order, never
// aliasing an input. It is nil when there are none.
func union(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		for _, s := range l {
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

// intersect narrows two non-empty allow lists to entries provably inside both.
// It never returns an empty slice.
func intersect(a, b []string) []string {
	var out []string
	add := func(s string) {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	for _, x := range a {
		for _, y := range b {
			switch {
			case x == y:
				add(x)
			case !isGlob(x) && matches(y, x):
				add(x)
			case !isGlob(y) && matches(x, y):
				add(y)
			}
		}
	}
	if len(out) == 0 {
		return []string{noTools}
	}
	return out
}

func isGlob(s string) bool { return strings.ContainsAny(s, `*?[\`) }

func matches(pattern, name string) bool {
	ok, err := path.Match(pattern, name)
	return err == nil && ok
}
