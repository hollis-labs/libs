package agentdef

import (
	"fmt"
	"strings"
)

// LintFinding is a non-fatal style/policy observation.
type LintFinding struct {
	Path, Rule, Message, Severity string // Severity: "warn" | "error"
}

// Lint rule names.
const (
	RuleDuplicateEntry      = "duplicate-entry"
	RuleRequiresUsesOverlap = "requires-uses-overlap"
	RuleDescriptionLabel    = "description-label"
	RuleEmptyBody           = "empty-body"
)

// minDescriptionWords is the heuristic floor below which a description reads
// as a label ("Code reviewer") rather than a trigger for delegation.
const minDescriptionWords = 5

// Lint reports style and policy findings that do not make a definition
// invalid. Path is the definition's SourceRef. Findings are in a stable order.
func Lint(d *Definition) []LintFinding {
	var out []LintFinding
	add := func(rule, format string, args ...any) {
		out = append(out, LintFinding{Path: d.SourceRef, Rule: rule, Message: fmt.Sprintf(format, args...), Severity: "warn"})
	}

	for _, f := range []struct {
		field   string
		entries []string
	}{
		{"skills", d.Skills}, {"tools", d.Tools}, {"requires", d.Requires},
		{"uses", d.Uses}, {"hooks", d.Hooks}, {"tags", d.Tags},
	} {
		seen := map[string]bool{}
		for _, e := range f.entries {
			if seen[e] {
				add(RuleDuplicateEntry, "%s lists %q more than once", f.field, e)
			}
			seen[e] = true
		}
	}

	req := map[string]bool{}
	for _, r := range d.Requires {
		req[r] = true
	}
	reported := map[string]bool{}
	for _, u := range d.Uses {
		if req[u] && !reported[u] {
			reported[u] = true
			add(RuleRequiresUsesOverlap, "%q is in both requires and uses; requires already implies it", u)
		}
	}

	if n := len(strings.Fields(d.Description)); d.Description != "" && n < minDescriptionWords {
		add(RuleDescriptionLabel, "description has %d words; say what the agent does and when to delegate to it", n)
	}
	if strings.TrimSpace(d.Body) == "" {
		add(RuleEmptyBody, "body is empty; the agent has no instructions")
	}
	return out
}
