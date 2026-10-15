package hooks

import (
	"fmt"
	"strings"
	"time"
)

// Kind is how a hook is executed.
type Kind string

// The two core kinds. There is deliberately no HTTP kind: it is Claude-only
// and not part of the core.
const (
	KindCommand Kind = "command"
	KindMCPTool Kind = "mcp_tool"
)

// Valid reports whether k is a core kind.
func (k Kind) Valid() bool { return k == KindCommand || k == KindMCPTool }

// OnError is a hook's declared failure mode. There is intentionally no
// default: the zero value is invalid and Hook.Validate rejects it.
type OnError string

const (
	// OnErrorOpen treats failure, timeout or invalid output as a no-op allow.
	OnErrorOpen OnError = "open"
	// OnErrorClosed treats failure, timeout or invalid output as a deny.
	OnErrorClosed OnError = "closed"
)

// Valid reports whether m is "open" or "closed".
func (m OnError) Valid() bool { return m == OnErrorOpen || m == OnErrorClosed }

// Layer is the registration source of a hook. Precedence is
// managed > user > project.
type Layer string

// The three layers.
const (
	LayerManaged Layer = "managed"
	LayerUser    Layer = "user"
	LayerProject Layer = "project"
)

// Valid reports whether l is one of the three layers.
func (l Layer) Valid() bool {
	return l == LayerManaged || l == LayerUser || l == LayerProject
}

// MCPToolRef names an mcp_tool-kind hook's target. go-hooks does not dial or
// discover it; that is the host's MCP client.
type MCPToolRef struct{ Server, Tool string }

// Hook is one registered implementation. A definition only carries hook
// names; the host's registry, built by resolving managed, user and project
// sources through Resolve, produces a []Hook keyed by Name.
type Hook struct {
	Name  string
	Event Event
	Kind  Kind
	// Matcher selects tool_name for PreToolUse, PostToolUse and
	// PermissionRequest using Claude Code's rule (see MatchesTool): "" or
	// "*" match all, a name or "A|B" / "A, B" list is exact, anything else
	// is an unanchored regular expression. Every other event fires
	// unconditionally regardless of Matcher.
	Matcher string
	// Command is an already-resolved argv[0] (Kind == command). go-hooks
	// does not resolve "skill:" or "host:" prefixes.
	Command     string
	CommandArgs []string
	// MCPTool is the target (Kind == mcp_tool).
	MCPTool MCPToolRef
	// Timeout is required and must be > 0.
	Timeout time.Duration
	// Async marks a fire-and-forget hook. The Runner does not special-case
	// it; the host engine decides whether its Output is honored.
	Async bool
	// AdditionalContextLimit is a byte cap; 0 means no lib-enforced cap. See
	// TruncateContext.
	AdditionalContextLimit int
	// OnError is REQUIRED; "" fails Validate.
	OnError OnError
	// Layer is set by Resolve, never hand-authored.
	Layer Layer
}

// Problem is one violated field reported by Validate.
type Problem struct {
	Field string
	Msg   string
}

// ValidationError lists every problem found in one Hook.
type ValidationError struct {
	Hook     string
	Problems []Problem
}

// Error implements error. It names every violated field.
func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		parts[i] = p.Field + ": " + p.Msg
	}
	name := e.Hook
	if name == "" {
		name = "(unnamed)"
	}
	return fmt.Sprintf("hooks: invalid hook %s: %s", name, strings.Join(parts, "; "))
}

// Fields returns the names of the violated fields in report order.
func (e *ValidationError) Fields() []string {
	out := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		out[i] = p.Field
	}
	return out
}

// Validate reports EVERY missing or invalid required field in one
// *ValidationError, not just the first. OnError == "" is always an error.
// Layer may be empty (Resolve sets it) but must be valid when present.
func (h Hook) Validate() error {
	var ps []Problem
	add := func(field, msg string) { ps = append(ps, Problem{field, msg}) }

	if h.Name == "" {
		add("Name", "required")
	}
	if h.Event == "" {
		add("Event", "required")
	} else if !h.Event.Valid() {
		add("Event", fmt.Sprintf("unknown event %q", string(h.Event)))
	}
	switch {
	case h.Kind == "":
		add("Kind", "required")
	case !h.Kind.Valid():
		add("Kind", fmt.Sprintf("unknown kind %q", string(h.Kind)))
	case h.Kind == KindCommand && h.Command == "":
		add("Command", "required for kind command")
	case h.Kind == KindMCPTool && (h.MCPTool.Server == "" || h.MCPTool.Tool == ""):
		add("MCPTool", "server and tool required for kind mcp_tool")
	}
	if h.Timeout <= 0 {
		add("Timeout", "must be > 0")
	}
	if h.OnError == "" {
		add("OnError", "required (open or closed); there is no default")
	} else if !h.OnError.Valid() {
		add("OnError", fmt.Sprintf("unknown mode %q (want open or closed)", string(h.OnError)))
	}
	if h.AdditionalContextLimit < 0 {
		add("AdditionalContextLimit", "must be >= 0")
	}
	if h.Layer != "" && !h.Layer.Valid() {
		add("Layer", fmt.Sprintf("unknown layer %q", string(h.Layer)))
	}
	if len(ps) == 0 {
		return nil
	}
	return &ValidationError{Hook: h.Name, Problems: ps}
}
