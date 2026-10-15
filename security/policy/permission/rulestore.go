package permission

import "context"

// RuleStore persists rules granted at ScopeProject. The engine ships no
// implementation: where and how project rules are stored (a YAML file, a
// database) is the host's decision. SaveRulesToFile and LoadRulesFromFile are
// building blocks for a file-backed one.
type RuleStore interface {
	// Append persists r for the project the session belongs to. The engine
	// calls it from Respond, after an allow at ScopeProject. It does not add
	// the rule to the engine's own RuleSet; call SetRules for that.
	Append(ctx context.Context, scope Scope, sessionID string, r Rule) error
}
