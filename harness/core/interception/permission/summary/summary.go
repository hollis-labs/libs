// summary.go renders a human-readable per-session permission summary
// suitable for inclusion in an agent's prompt or context window.
//
// Background. Path-access rules (an allow-list, session-scoped path grants,
// lineage-inherited grants and deny rules) are enforced in code but are
// invisible to the model unless rendered into its prompt; an agent that
// cannot see its constraints reasons about file access from priors and
// confabulates when the priors are wrong. This renderer makes the
// constraints visible so the agent can refuse rather than fabricate.
//
// Sharp edges enforced here:
//
//   - "Cannot access" listings are bounded. Enumerating the whole filesystem
//     is infinite in principle; the summary surfaces only the explicit
//     deny-mode rules from the resolved RuleSet, plus a single closing
//     sentence stating the implicit-deny default ("any path not listed
//     above is outside session scope"). Callers MUST resolve workspace-
//     relative `./` patterns via `(*permission.RuleSet).Resolve(workingDir)` before
//     passing the RuleSet here — the renderer prints patterns verbatim and
//     would otherwise leak un-resolved `./` shapes into the prompt.
//
//   - Source provenance is preserved. Each rendered row tags the rule's
//     origin (Rule.Source) and each path grant tags its source ("session
//     grants" / "inherited from parent session"). This mirrors what Resolve
//     and DeriveSubagentRuleSet make available when forwarding parent
//     denies into spawned children.
//
//   - Defense in depth. This renderer is the upstream PREVENTION layer —
//     visible constraints so the agent can REFUSE rather than fabricate.
//     It does NOT replace the runtime gate (Engine.Check) or a tool's own
//     path-escape checks.
//
//   - Determinism. RenderPermissionSummary is a pure projection of its
//     inputs. The output is stable across turns when the inputs don't
//     change, so a content-hash cache key over the output is stable until
//     the grants shift or the resolved RuleSet changes, keeping a cacheable
//     prompt prefix intact.
package summary

import (
	"path/filepath"
	"sort"
	"strings"

	permission "github.com/hollis-labs/substrate/harness/interception/permission"
)

// SummaryInput aggregates the data sources the permission summary draws
// from. All fields are optional — an empty input produces an empty string
// from RenderPermissionSummary so the caller can skip the section when the
// agent has no relevant constraints to surface.
//
// WorkingDir is the session's working directory (already canonicalized by
// the caller). Used only to render the "rooted at" hint when path patterns
// reference it; the actual `./` → absolute resolution must have happened
// BEFORE the RuleSet reaches this renderer (call (*permission.RuleSet).Resolve
// at session start).
//
// Rules carries the resolved permission RuleSet (deny + ask + allow rules)
// with workspace-relative patterns already expanded to absolute form. The
// renderer groups by Behavior (deny → ask → allow) so the agent reads
// constraints before affordances.
//
// AllowedPaths is the host's static allow-list: the directories its file
// tools accept by default before consulting session grants. Rendered as the
// baseline READ/WRITE roots.
//
// OwnGrants and InheritedGrants carry the session-scoped explicit-mention
// path grants (cleaned absolute paths). OwnGrants is the session's own
// bucket; InheritedGrants is the union of any ancestor sessions reachable
// via PathGrants.RegisterLineage (parent chat session for a spawned
// subagent). Both are rendered with provenance tags so the agent sees the
// origin of each grant.
//
// SessionScope, when non-empty, is rendered as the closing "any path not
// listed above is outside session scope" sentence's qualifier (e.g.
// "this researcher subagent's scope"). Empty produces the generic phrasing.
type SummaryInput struct {
	WorkingDir      string
	Rules           *permission.RuleSet
	AllowedPaths    []string
	OwnGrants       []string
	InheritedGrants []string
	SessionScope    string
}

// RenderPermissionSummary returns a human-readable Markdown block describing
// the effective path access for the session. Empty input → empty string
// (the caller skips the section). The returned content has no trailing
// newline.
//
// Output shape (sections present only when content exists):
//
//	## Path access
//
//	You can READ under (default workspace allow-list):
//	  - /Users/.../project_a/
//	  - /Users/.../project_b/
//
//	You have explicit grants for (session):
//	  - /Users/.../user-mentioned/path.go  (from session grants)
//
//	You inherit these from the parent session:
//	  - /Users/.../parent-mentioned/  (from parent chat session)
//
//	You CANNOT access (explicitly denied):
//	  - /Users/.../sensitive/**  (from profile.yaml)
//
//	Any path not listed above is outside session scope. If a task needs an
//	out-of-scope path, return a failure naming the path rather than
//	synthesizing an answer from training data.
//
// The closing instruction states the refusal rule at the point of decision —
// agents read the constraint immediately before deciding whether to attempt
// a tool call.
//
// Cache behavior. The output is deterministic over (Rules, AllowedPaths,
// OwnGrants, InheritedGrants, SessionScope). When any of those change the
// output changes; otherwise it ships verbatim across turns.
func RenderPermissionSummary(in SummaryInput) string {
	allowedPaths := normalizeAllowedPaths(in.AllowedPaths)
	ownGrants := dedupSortedCopy(in.OwnGrants)
	inheritedGrants := subtractAndDedup(in.InheritedGrants, ownGrants)
	denyRules, askRules, allowRules := classifyRules(in.Rules)

	// If nothing to render, return empty so the section is skipped entirely.
	// The "## Path access" header alone is not worth burning tokens on.
	if len(allowedPaths) == 0 && len(ownGrants) == 0 && len(inheritedGrants) == 0 &&
		len(denyRules) == 0 && len(askRules) == 0 && len(allowRules) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Path access\n")

	// Allow-side — surface affordances first so the agent reads "you can"
	// before "you cannot". The bounded "cannot access" listing follows.
	if len(allowedPaths) > 0 {
		b.WriteString("\nYou can READ under (workspace allow-list):\n")
		for _, p := range allowedPaths {
			b.WriteString("  - ")
			b.WriteString(p)
			b.WriteString("\n")
		}
	}

	if len(ownGrants) > 0 {
		b.WriteString("\nYou have explicit access to (session grants):\n")
		for _, p := range ownGrants {
			b.WriteString("  - ")
			b.WriteString(p)
			b.WriteString("\n")
		}
	}

	if len(inheritedGrants) > 0 {
		b.WriteString("\nYou inherit these from the parent session:\n")
		for _, p := range inheritedGrants {
			b.WriteString("  - ")
			b.WriteString(p)
			b.WriteString("  (from parent session)\n")
		}
	}

	if len(allowRules) > 0 {
		b.WriteString("\nAdditional allow rules from profile:\n")
		writeRuleList(&b, allowRules)
	}

	// Ask-rule visibility. These aren't outright denies, but the agent
	// should know they will pause and prompt — surface so it can avoid
	// the interrupt or pre-announce the upcoming approval prompt.
	if len(askRules) > 0 {
		b.WriteString("\nThese will require approval:\n")
		writeRuleList(&b, askRules)
	}

	// Deny-side. Bounded enumeration — only explicit deny rules from the
	// resolved RuleSet. The infinite "everything else" is communicated by
	// the closing implicit-deny sentence rather than enumerated.
	if len(denyRules) > 0 {
		b.WriteString("\nYou CANNOT access (explicitly denied):\n")
		writeRuleList(&b, denyRules)
	}

	// Closing refusal hook. Echoes the universal "Refusal" rules at the
	// point of decision so the agent reads the constraint immediately
	// before deciding whether to attempt a tool call.
	scope := strings.TrimSpace(in.SessionScope)
	scopeQualifier := "this session's scope"
	if scope != "" {
		scopeQualifier = scope
	}
	b.WriteString("\nAny path not listed above is outside ")
	b.WriteString(scopeQualifier)
	b.WriteString(". If a task needs an out-of-scope path, return a failure naming the path rather than synthesizing an answer.")

	return b.String()
}

// classifyRules groups rs.Rules by Behavior (deny / ask / allow) in the
// same priority order as Evaluate(). Returns three slices that preserve
// the input order within each group — Source tags ride along so the
// renderer can attribute each row.
//
// Nil-safe: a nil RuleSet returns three empty slices.
func classifyRules(rs *permission.RuleSet) (deny, ask, allow []permission.Rule) {
	if rs == nil {
		return nil, nil, nil
	}
	for _, r := range rs.Rules {
		switch r.Behavior {
		case permission.DecisionDeny:
			deny = append(deny, r)
		case permission.DecisionAsk:
			ask = append(ask, r)
		case permission.DecisionAllow:
			allow = append(allow, r)
		}
	}
	return deny, ask, allow
}

// writeRuleList renders a slice of rules as Markdown bullets, attributing
// each rule's source. The Tool + Pattern shape mirrors the YAML rule the
// user (or agent profile) authored, so debugging back from the rendered
// summary to the source rule is one greptable hop.
//
// Output per rule:
//   - `read_file` on `/path/**`  (from profile.yaml)
//   - `write_file` on `./generated/**`  (from session permissions)
//
// When Source is empty (a rule built in-memory at runtime without a
// provenance tag), the suffix is "(unknown source)" so the renderer
// stays deterministic instead of breaking out of the bullet shape.
func writeRuleList(b *strings.Builder, rules []permission.Rule) {
	for _, r := range rules {
		b.WriteString("  - ")
		if r.Tool != "" {
			b.WriteString("`")
			b.WriteString(r.Tool)
			b.WriteString("`")
		} else {
			b.WriteString("`(any tool)`")
		}
		if r.Pattern != "" {
			b.WriteString(" on `")
			b.WriteString(r.Pattern)
			b.WriteString("`")
		}
		source := strings.TrimSpace(r.Source)
		if source == "" {
			source = "unknown source"
		}
		b.WriteString("  (from ")
		b.WriteString(source)
		b.WriteString(")\n")
	}
}

// normalizeAllowedPaths returns a sorted, deduplicated, trimmed-with-
// trailing-separator copy of paths. The trailing separator makes the
// "everything under here" semantic explicit in the rendered output —
// agents read "/foo/bar/" as a directory root, "/foo/bar" as ambiguous.
//
// Empty / whitespace-only entries are dropped. Order is sorted so the
// output is deterministic regardless of the input slice's order.
func normalizeAllowedPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Clean the path; append a trailing OS separator so the rendered
		// row reads as a directory root rather than an ambiguous file.
		clean := filepath.Clean(p)
		sep := string(filepath.Separator)
		if !strings.HasSuffix(clean, sep) {
			clean += sep
		}
		if _, dup := seen[clean]; dup {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	sort.Strings(out)
	return out
}

// dedupSortedCopy returns a sorted, deduplicated copy of paths with
// whitespace-only entries dropped. Used for grant lists where the input
// is already cleaned (PathGrants.ListGrants only stores cleaned absolute
// paths) but order is unspecified.
func dedupSortedCopy(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// subtractAndDedup returns inherited \ own (set difference), sorted +
// deduplicated. A grant the worker already has in its own bucket is
// suppressed from the "inherited" section so each path appears exactly
// once in the rendered output — under "session grants" if local, under
// "inherited" if only present on a parent.
func subtractAndDedup(inherited, own []string) []string {
	if len(inherited) == 0 {
		return nil
	}
	ownSet := make(map[string]struct{}, len(own))
	for _, p := range own {
		ownSet[strings.TrimSpace(p)] = struct{}{}
	}
	seen := make(map[string]struct{}, len(inherited))
	out := make([]string, 0, len(inherited))
	for _, p := range inherited {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, dup := ownSet[p]; dup {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
