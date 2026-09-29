package permission

// Scope determines how long a permission grant persists.
type Scope string

// The Scope values.
const (
	// ScopeOnce grants permission for this single invocation only.
	ScopeOnce Scope = "once"
	// ScopeSession grants permission for the remainder of this session.
	ScopeSession Scope = "session"
	// ScopeProject persists permission through the engine's RuleStore. With
	// no RuleStore it behaves as ScopeOnce.
	ScopeProject Scope = "project"
)
