package profile

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

// ErrUnknownServer is returned (wrapped) when Profile.Servers names a server
// id that is not in the catalog.
var ErrUnknownServer = errors.New("toolselect/profile: unknown server id in profile")

// ErrBadPattern is returned (wrapped) when Profile.ToolsAllow or ToolsDeny
// holds a malformed path.Match glob.
var ErrBadPattern = errors.New("toolselect/profile: malformed glob pattern in profile")

// Server is one MCP server/origin as the evaluator sees it. Its position in
// Catalog.Servers is the second sort key of Evaluate's output order.
type Server struct {
	ID    string
	Tools []Tool
}

// Tool is one tool of a Server.
type Tool struct {
	Name  string
	Title string
	// ReadOnly and Destructive are verbatim MCP hints; nil means undeclared.
	ReadOnly    *bool
	Destructive *bool
}

// Catalog is every server the gateway has, enabled or not, in the host's
// declared server order.
type Catalog struct {
	Servers []Server
}

// Profile is the evaluator's input. The zero value is valid: no profile
// means every tool of every server is visible.
type Profile struct {
	ID string

	// Servers maps a server id to enabled (true) or disabled (false). An id
	// absent from the map is enabled. An id present in the map but not in
	// the catalog is ErrUnknownServer.
	Servers map[string]bool

	// ToolsAllow and ToolsDeny are path.Match globs on Tool.Name. An empty
	// ToolsAllow allows every tool of every enabled server.
	ToolsAllow []string
	ToolsDeny  []string

	// ReadOnly, when true, hides any tool whose ReadOnly hint is not exactly
	// true; a nil hint is hidden. Advisory only.
	ReadOnly bool

	// Order lists exact tool names to pin first, in this order.
	Order []string
	// AlwaysLoad lists exact tool names flagged VisibleTool.AlwaysLoad. It is
	// informational and does not affect position.
	AlwaysLoad []string

	// Instructions is opaque passthrough; this package never reads it.
	Instructions string
}

// VisibleTool is one entry of Evaluate's visible output.
type VisibleTool struct {
	Server string
	Name   string
	Title  string
	// ReadOnly and Destructive are copied verbatim from the catalog tool.
	ReadOnly    *bool
	Destructive *bool
	AlwaysLoad  bool
}

// HiddenCause says why a tool is hidden.
type HiddenCause string

// The hidden causes.
const (
	CauseServerDisabled HiddenCause = "server_disabled"
	CauseDenied         HiddenCause = "denied"      // matched Profile.ToolsDeny
	CauseReadOnly       HiddenCause = "read_only"   // Profile.ReadOnly and the hint is not exactly true
	CauseNotAllowed     HiddenCause = "not_allowed" // ToolsAllow is non-empty and nothing in it matched
)

// HiddenReason explains why a cataloged tool did not reach the visible list.
type HiddenReason struct {
	Server string
	Name   string
	Reason HiddenCause
}

// Evaluate applies profile to catalog. It is pure: the same inputs always
// produce the same outputs.
//
// Precedence is server disabled, then deny, read_only, allow. Visible tools
// are ordered by Profile.Order position (listed names first, in that order),
// then by server position in catalog.Servers, then by Tool.Name. Hidden
// reasons are in catalog order. On error, visible and hidden are nil.
func Evaluate(catalog Catalog, profile Profile) (visible []VisibleTool, hidden []HiddenReason, err error) {
	known := make(map[string]struct{}, len(catalog.Servers))
	for _, s := range catalog.Servers {
		known[s.ID] = struct{}{}
	}
	var unknown []string
	for id := range profile.Servers {
		if _, ok := known[id]; !ok {
			unknown = append(unknown, fmt.Sprintf("%q", id))
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, nil, fmt.Errorf("%w: %s", ErrUnknownServer, strings.Join(unknown, ", "))
	}
	for _, list := range [][]string{profile.ToolsAllow, profile.ToolsDeny} {
		for _, p := range list {
			if _, perr := path.Match(p, ""); perr != nil {
				return nil, nil, fmt.Errorf("%w: %q", ErrBadPattern, p)
			}
		}
	}

	pinAt := make(map[string]int, len(profile.Order))
	for i, name := range profile.Order {
		if _, dup := pinAt[name]; !dup {
			pinAt[name] = i
		}
	}
	always := make(map[string]struct{}, len(profile.AlwaysLoad))
	for _, name := range profile.AlwaysLoad {
		always[name] = struct{}{}
	}

	type entry struct {
		pin, server int
		v           VisibleTool
	}
	var entries []entry
	for si, s := range catalog.Servers {
		enabled := true
		if on, set := profile.Servers[s.ID]; set {
			enabled = on
		}
		for _, t := range s.Tools {
			if !enabled {
				hidden = append(hidden, HiddenReason{s.ID, t.Name, CauseServerDisabled})
				continue
			}
			if cause := hide(t, profile); cause != "" {
				hidden = append(hidden, HiddenReason{s.ID, t.Name, cause})
				continue
			}
			pin, pinned := pinAt[t.Name]
			if !pinned {
				pin = len(profile.Order)
			}
			_, al := always[t.Name]
			entries = append(entries, entry{pin, si, VisibleTool{
				Server: s.ID, Name: t.Name, Title: t.Title,
				ReadOnly: t.ReadOnly, Destructive: t.Destructive, AlwaysLoad: al,
			}})
		}
	}
	sort.SliceStable(entries, func(a, b int) bool {
		x, y := entries[a], entries[b]
		if x.pin != y.pin {
			return x.pin < y.pin
		}
		if x.server != y.server {
			return x.server < y.server
		}
		return x.v.Name < y.v.Name
	})
	for _, e := range entries {
		visible = append(visible, e.v)
	}
	return visible, hidden, nil
}

// hide returns the cause that hides t, or "" if t is visible.
func hide(t Tool, p Profile) HiddenCause {
	if matchAny(p.ToolsDeny, t.Name) {
		return CauseDenied
	}
	if p.ReadOnly && (t.ReadOnly == nil || !*t.ReadOnly) {
		return CauseReadOnly
	}
	if len(p.ToolsAllow) > 0 && !matchAny(p.ToolsAllow, t.Name) {
		return CauseNotAllowed
	}
	return ""
}

func matchAny(globs []string, name string) bool {
	for _, g := range globs {
		if ok, err := path.Match(g, name); err == nil && ok {
			return true
		}
	}
	return false
}
