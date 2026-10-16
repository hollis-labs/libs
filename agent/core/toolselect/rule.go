package toolselect

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
)

// ErrInvalidRule is returned (wrapped) by Rank for a rule with an unknown
// action type.
var ErrInvalidRule = errors.New("toolselect: invalid rule")

// Rule is the shared selection-limiting schema.
type Rule struct {
	Name string
	// Intent is a path.Match glob tested against the query string. "" or "*"
	// matches every query; a malformed glob matches none.
	Intent   string
	Match    Match
	Action   Action
	Priority int // higher runs first; equal priorities keep declaration order
}

// Match identifies tools for a rule. All non-empty fields AND together;
// within a field any element matching is enough. An entirely empty Match
// matches every tool.
type Match struct {
	// Patterns are path.Match globs on Tool.Name. A malformed pattern
	// matches nothing.
	Patterns []string
	// Tags match a tool that carries any of them (exact, case-sensitive).
	Tags []string
	// Servers match Tool.Server exactly.
	Servers []string
}

// ActionType is what a rule does with the tools it matches.
type ActionType string

// The action types.
const (
	// ActionInclude is a whitelist: once any applicable include rule exists,
	// only tools matched by an include rule survive.
	ActionInclude ActionType = "include"
	// ActionExclude drops matched tools regardless of any include.
	ActionExclude ActionType = "exclude"
	// ActionOrder pins matched tools first, in Action.Pin order. It never
	// changes membership.
	ActionOrder ActionType = "order"
)

// Action is a rule's effect.
type Action struct {
	Type ActionType
	// Pin, for ActionOrder only, lists tool names (exact, not globs) in the
	// order they should lead the output. A name that is not matched by the
	// rule's Match or is not in the candidate set is skipped without error.
	// Matched tools not listed keep their ranked order.
	Pin []string
}

func validateRules(rules []Rule) error {
	for i, r := range rules {
		switch r.Action.Type {
		case ActionInclude, ActionExclude, ActionOrder:
		default:
			return fmt.Errorf("%w: rule %d (%q): unknown action type %q", ErrInvalidRule, i, r.Name, r.Action.Type)
		}
	}
	return nil
}

// applicableRules returns the rules whose Intent matches query, highest
// priority first, equal priorities in declaration order.
func applicableRules(rules []Rule, query string) []Rule {
	var out []Rule
	for _, r := range rules {
		if intentMatches(r.Intent, query) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Priority > out[j].Priority })
	return out
}

func intentMatches(pattern, query string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	ok, err := path.Match(pattern, query)
	return err == nil && ok
}

func toolMatches(t *Tool, m Match) bool {
	if len(m.Patterns) > 0 {
		found := false
		for _, p := range m.Patterns {
			if ok, err := path.Match(p, t.Name); err == nil && ok {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(m.Tags) > 0 {
		found := false
		for _, tag := range m.Tags {
			if slices.Contains(t.Tags, tag) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(m.Servers) > 0 && !slices.Contains(m.Servers, t.Server) {
		return false
	}
	return true
}
