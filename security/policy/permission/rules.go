package permission

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Rule is a single permission rule that matches tool invocations.
type Rule struct {
	Tool     string   `yaml:"tool"`     // tool name or glob pattern
	Pattern  string   `yaml:"pattern"`  // input pattern (glob on first string arg)
	Behavior Decision `yaml:"behavior"` // allow, deny, ask
	Source   string   `yaml:"-"`        // where this rule came from (for debugging)
}

// RuleSet holds an ordered list of rules and the permission mode.
type RuleSet struct {
	Mode  Mode   `yaml:"mode"`
	Rules []Rule `yaml:"rules"`
}

// PermissionsFile is the YAML structure of a permissions file: a top-level
// `permissions:` key holding a RuleSet.
type PermissionsFile struct {
	Permissions RuleSet `yaml:"permissions"`
}

// Evaluate checks rules against a tool invocation using the default Matcher.
// It returns the first matching rule's result, or nil if no rule matches. The
// result's MatchedRule is a copy, so callers cannot mutate the RuleSet
// through it.
//
// Evaluation priority: deny > ask > allow. Within the same behavior, first
// match wins. A rule whose Behavior is none of the three never fires; use
// Validate to catch that.
func (rs *RuleSet) Evaluate(toolName string, input map[string]any) *CheckResult {
	return rs.evaluate(nil, toolName, input)
}

// evaluate is Evaluate with an optional Matcher (nil means defaults). It makes
// a single pass and allocates only for the returned result.
func (rs *RuleSet) evaluate(m *Matcher, toolName string, input map[string]any) *CheckResult {
	if rs == nil {
		return nil
	}
	var deny, ask, allow *Rule
	for i := range rs.Rules {
		r := &rs.Rules[i]
		switch r.Behavior {
		case DecisionDeny:
			if deny != nil {
				continue
			}
		case DecisionAsk:
			if ask != nil {
				continue
			}
		case DecisionAllow:
			if allow != nil {
				continue
			}
		default:
			continue
		}
		if !r.matches(m, toolName, input) {
			continue
		}
		switch r.Behavior {
		case DecisionDeny:
			deny = r
		case DecisionAsk:
			ask = r
		case DecisionAllow:
			allow = r
		}
		if deny != nil {
			break // deny is the top tier; nothing can outrank it
		}
	}

	switch {
	case deny != nil:
		return matchResult(deny, DecisionDeny, "denied by rule")
	case ask != nil:
		return matchResult(ask, DecisionAsk, "requires approval")
	case allow != nil:
		return matchResult(allow, DecisionAllow, "allowed by rule")
	}
	return nil
}

func matchResult(r *Rule, d Decision, prefix string) *CheckResult {
	cp := *r
	return &CheckResult{
		Decision:    d,
		MatchedRule: &cp,
		Reason:      fmt.Sprintf("%s: tool=%s pattern=%s", prefix, r.Tool, r.Pattern),
	}
}

// Matches checks if a rule matches the given tool name and input using the
// default Matcher.
func (r *Rule) Matches(toolName string, input map[string]any) bool {
	return r.matches(nil, toolName, input)
}

func (r *Rule) matches(m *Matcher, toolName string, input map[string]any) bool {
	// Match tool name (supports glob patterns).
	if !matchGlob(r.Tool, toolName) {
		return false
	}

	// If no pattern, tool name match is sufficient.
	if r.Pattern == "" {
		return true
	}

	// Match pattern against input.
	return m.matchInput(r.Pattern, input)
}

// Validate reports rules that would silently never fire or never match: an
// unknown Behavior (for example the typo "dney", which Evaluate ignores, so
// the deny rule it was meant to be never applies), a malformed Tool glob, or
// an unknown Mode. It returns every problem found, joined, or nil. It is
// nil-safe. Pattern is not checked: it is a path glob for path keys and a
// plain substring for command keys, so a string that is a bad glob can still
// be a valid command pattern.
func (rs *RuleSet) Validate() error {
	if rs == nil {
		return nil
	}
	var errs []error
	switch rs.Mode {
	case "", ModeDefault, ModeAcceptEdits, ModePlan, ModeYolo:
	default:
		errs = append(errs, fmt.Errorf("unknown mode %q", rs.Mode))
	}
	for i, r := range rs.Rules {
		switch r.Behavior {
		case DecisionAllow, DecisionDeny, DecisionAsk:
		default:
			errs = append(errs, fmt.Errorf("rule %d (tool=%q): unknown behavior %q", i, r.Tool, r.Behavior))
		}
		if _, err := filepath.Match(r.Tool, ""); err != nil {
			errs = append(errs, fmt.Errorf("rule %d (tool=%q): malformed tool glob: %w", i, r.Tool, err))
		}
	}
	return errors.Join(errs...)
}

// matchGlob does simple glob matching of a tool name. An empty pattern or "*"
// matches everything. A malformed pattern matches nothing (RuleSet.Validate
// reports it).
func matchGlob(pattern, name string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	matched, err := filepath.Match(pattern, name)
	if err != nil {
		return false
	}
	return matched
}

// Matcher controls how a rule's Pattern is matched against tool input. The
// zero value uses the defaults below. Pass one to an Engine with WithMatcher.
//
// Sharp edges, both by design:
//
//   - A Pattern is only ever compared with input values stored under
//     PathKeys or CommandKeys. A tool whose input uses another key (for
//     example "paths", "url" or "cmd") never matches a rule that has a
//     Pattern, so a deny rule with a Pattern silently does not apply to it.
//     Extend the keys to cover your tools rather than assuming coverage.
//   - The default command match is a plain substring test and is advisory:
//     an allow pattern "git" also matches "git; rm -rf ~". Do not rely on
//     it as a security boundary; supply a Command func that tokenises the
//     command if you need more.
type Matcher struct {
	// PathKeys are the input keys whose string value is matched as a path
	// glob. Nil means path, file, directory, file_path; an empty non-nil
	// slice disables path matching.
	PathKeys []string
	// CommandKeys are the input keys whose string value is matched with
	// Command. Nil means command; an empty non-nil slice disables it.
	CommandKeys []string
	// Command reports whether pattern matches command. Nil means substring
	// (strings.Contains).
	Command func(pattern, command string) bool
}

var (
	defaultPathKeys    = []string{"path", "file", "directory", "file_path"}
	defaultCommandKeys = []string{"command"}
)

// matchInput reports whether pattern matches any recognized input value. A nil
// Matcher means the defaults.
func (m *Matcher) matchInput(pattern string, input map[string]any) bool {
	pathKeys, commandKeys := defaultPathKeys, defaultCommandKeys
	var command func(string, string) bool
	if m != nil {
		if m.PathKeys != nil {
			pathKeys = m.PathKeys
		}
		if m.CommandKeys != nil {
			commandKeys = m.CommandKeys
		}
		command = m.Command
	}
	if command == nil {
		// strings.Contains(s, substr): argument order is (command, pattern).
		command = func(pattern, cmd string) bool { return strings.Contains(cmd, pattern) }
	}

	for _, key := range pathKeys {
		if s, ok := input[key].(string); ok && matchPathGlob(pattern, s) {
			return true
		}
	}
	for _, key := range commandKeys {
		if s, ok := input[key].(string); ok && command(pattern, s) {
			return true
		}
	}
	return false
}

// matchPathGlob matches a glob pattern against a file path. Besides
// filepath.Match syntax it understands two forms of "**":
//
//   - A trailing "/**" matches the directory itself and everything under it,
//     on a path-segment boundary: "/work/proj/**" matches "/work/proj" and
//     "/work/proj/a/b" but not "/work/proj-secret/x".
//   - A leading "**/" matches the remainder against the base name or the full
//     path: "**/*.env" matches "/a/b/.env".
func matchPathGlob(pattern, path string) bool {
	if matched, err := filepath.Match(pattern, path); err == nil && matched {
		return true
	}
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	if strings.HasPrefix(pattern, "**/") {
		suffix := strings.TrimPrefix(pattern, "**/")
		if matched, err := filepath.Match(suffix, filepath.Base(path)); err == nil && matched {
			return true
		}
		if matched, err := filepath.Match(suffix, path); err == nil && matched {
			return true
		}
	}
	return false
}

// LoadRulesFromFile reads a permissions YAML file, tags each rule's Source with
// the path, and runs Validate on the result.
func LoadRulesFromFile(path string) (*RuleSet, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the caller chooses which permissions file to load
	if err != nil {
		return nil, fmt.Errorf("read permissions file: %w", err)
	}

	var pf PermissionsFile
	if err := yaml.Unmarshal(data, &pf); err != nil {
		return nil, fmt.Errorf("parse permissions file: %w", err)
	}

	// Tag rules with their source.
	for i := range pf.Permissions.Rules {
		pf.Permissions.Rules[i].Source = path
	}

	if err := pf.Permissions.Validate(); err != nil {
		return nil, fmt.Errorf("invalid permissions file %s: %w", path, err)
	}

	return &pf.Permissions, nil
}

// MergeRuleSets merges multiple rule sets in priority order (first = highest).
// Mode comes from the highest-priority set that has one specified.
func MergeRuleSets(sets ...*RuleSet) *RuleSet {
	merged := &RuleSet{}
	for _, rs := range sets {
		if rs == nil {
			continue
		}
		if merged.Mode == "" && rs.Mode != "" {
			merged.Mode = rs.Mode
		}
		merged.Rules = append(merged.Rules, rs.Rules...)
	}
	return merged
}

// SaveRulesToFile writes a permissions YAML file.
func SaveRulesToFile(path string, rs *RuleSet) error {
	pf := PermissionsFile{Permissions: *rs}
	data, err := yaml.Marshal(pf)
	if err != nil {
		return fmt.Errorf("marshal permissions: %w", err)
	}
	dir := filepath.Dir(path)
	// The file is a shared, non-secret project artifact; 0755/0644 (world-
	// readable) is the long-standing behavior, so it is kept.
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: shared non-secret permissions file
		return fmt.Errorf("create permissions dir: %w", err)
	}
	return os.WriteFile(path, data, 0o644) //nolint:gosec // G306: shared non-secret permissions file
}
