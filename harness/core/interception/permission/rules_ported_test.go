package permission

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"", "anything", true},
		{"*", "anything", true},
		{"bash", "bash", true},
		{"bash", "python", false},
		{"mcp__*", "mcp__dev__edit", true},
		{"mcp__*", "dev_edit", false},
		// A malformed pattern matches nothing (it does not fall back to
		// literal equality).
		{"[", "[", false},
		{"a[", "a[", false},
	}
	for _, tc := range cases {
		if got := matchGlob(tc.pattern, tc.name); got != tc.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

func TestRuleMatchesFilePath(t *testing.T) {
	r := Rule{Tool: "*", Pattern: "/src/**", Behavior: DecisionAllow}
	if !r.Matches("edit", map[string]any{"file_path": "/src/main.go"}) {
		t.Error("file_path under /src should match")
	}
	if r.Matches("edit", map[string]any{"file_path": "/etc/passwd"}) {
		t.Error("file_path outside /src should not match")
	}
}

func TestRuleMatchesWildcard(t *testing.T) {
	r := Rule{Tool: "*", Behavior: DecisionAllow}
	if !r.Matches("anything", nil) || !r.Matches("bash", map[string]any{"path": "/foo"}) {
		t.Error("wildcard rule should match any tool")
	}
}

func TestRuleMatchesEmptyTool(t *testing.T) {
	r := Rule{Tool: "", Behavior: DecisionAllow}
	if !r.Matches("bash", nil) || !r.Matches("anything", nil) {
		t.Error("empty tool should match any tool")
	}
}

func TestLoadRulesFromFileNotFound(t *testing.T) {
	_, err := LoadRulesFromFile("/nonexistent/path/permissions.yaml")
	if err == nil {
		t.Fatal("expected an error")
	}
	// Errors are wrapped, so callers can still test the cause.
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error does not wrap os.ErrNotExist: %v", err)
	}
}

func TestMergeRuleSetsNilSafe(t *testing.T) {
	a := &RuleSet{Mode: ModeDefault, Rules: []Rule{{Tool: "bash", Behavior: DecisionAllow}}}
	merged := MergeRuleSets(nil, a, nil)
	if merged.Mode != ModeDefault || len(merged.Rules) != 1 {
		t.Errorf("merged = %+v", merged)
	}
	merged2 := MergeRuleSets(nil, nil)
	if merged2 == nil || len(merged2.Rules) != 0 {
		t.Errorf("all-nil merge = %+v", merged2)
	}
}

func TestPathGlobSegmentBoundary(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		// /** matches the directory itself and everything below it...
		{"/work/proj/**", "/work/proj", true},
		{"/work/proj/**", "/work/proj/a/b", true},
		{"/work/proj/**", "/work/proj/x", true},
		// ...but not a sibling that merely shares the prefix.
		{"/work/proj/**", "/work/proj-secret/x", false},
		{"/work/proj/**", "/work/proj-secret", false},
		{"/work/proj/**", "/work/other/x", false},
		{"/**", "/anything/at/all", true},
		// **/ prefix: base name or full path.
		{"**/*.env", "/a/b/.env", true},
		{"**/*.env", "prod.env", true},
		{"**/*.env", "/a/b/env.txt", false},
		{"**/*.env", "/a/b/c.env/d", false},
		{"**/secret.txt", "/x/y/secret.txt", true},
		{"**/secret.txt", "/x/y/other.txt", false},
		// Plain filepath.Match behaviour is unchanged.
		{"/tmp/*.txt", "/tmp/a.txt", true},
		{"/tmp/*.txt", "/tmp/sub/a.txt", false},
		// A malformed pattern never matches.
		{"[", "[", false},
	}
	for _, tc := range cases {
		if got := matchPathGlob(tc.pattern, tc.path); got != tc.want {
			t.Errorf("matchPathGlob(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func TestRuleDoesNotOvermatchSiblingDir(t *testing.T) {
	deny := &RuleSet{Rules: []Rule{{Tool: "*", Pattern: "/work/proj/**", Behavior: DecisionDeny}}}
	if r := deny.Evaluate("read", map[string]any{"path": "/work/proj-secret/x"}); r != nil {
		t.Errorf("sibling directory matched: %+v", r)
	}
	if r := deny.Evaluate("read", map[string]any{"path": "/work/proj/a/b"}); r == nil || r.Decision != DecisionDeny {
		t.Errorf("path under the directory should be denied: %+v", r)
	}
}

func TestEvaluateReturnsCopy(t *testing.T) {
	rs := &RuleSet{Rules: []Rule{{Tool: "bash", Behavior: DecisionDeny, Source: "orig"}}}
	res := rs.Evaluate("bash", nil)
	if res == nil || res.MatchedRule == nil {
		t.Fatal("expected a match")
	}
	res.MatchedRule.Source = "mutated"
	res.MatchedRule.Behavior = DecisionAllow
	if rs.Rules[0].Source != "orig" || rs.Rules[0].Behavior != DecisionDeny {
		t.Errorf("mutating MatchedRule changed the RuleSet: %+v", rs.Rules[0])
	}
}

func TestEvaluateFirstMatchPerTier(t *testing.T) {
	rs := &RuleSet{Rules: []Rule{
		{Tool: "*", Behavior: DecisionAllow, Source: "allow-1"},
		{Tool: "*", Behavior: DecisionAsk, Source: "ask-1"},
		{Tool: "*", Behavior: DecisionAsk, Source: "ask-2"},
		{Tool: "*", Behavior: DecisionDeny, Source: "deny-1"},
		{Tool: "*", Behavior: DecisionDeny, Source: "deny-2"},
	}}
	if r := rs.Evaluate("x", nil); r.MatchedRule.Source != "deny-1" {
		t.Errorf("got %s, want deny-1", r.MatchedRule.Source)
	}
	rs.Rules = rs.Rules[:3]
	if r := rs.Evaluate("x", nil); r.MatchedRule.Source != "ask-1" {
		t.Errorf("got %s, want ask-1", r.MatchedRule.Source)
	}
	// Nil RuleSet is safe.
	var nilRS *RuleSet
	if nilRS.Evaluate("x", nil) != nil {
		t.Error("nil RuleSet should not match")
	}
}

func TestEvaluateReasonText(t *testing.T) {
	rs := &RuleSet{Rules: []Rule{
		{Tool: "a", Pattern: "p", Behavior: DecisionDeny},
		{Tool: "b", Behavior: DecisionAsk},
		{Tool: "c", Behavior: DecisionAllow},
	}}
	for tool, want := range map[string]string{
		"a": "denied by rule: tool=a pattern=p",
		"b": "requires approval: tool=b pattern=",
		"c": "allowed by rule: tool=c pattern=",
	} {
		in := map[string]any{"command": "p"}
		if got := rs.Evaluate(tool, in).Reason; got != want {
			t.Errorf("%s: reason %q, want %q", tool, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	good := &RuleSet{Mode: ModePlan, Rules: []Rule{
		{Tool: "dev_*", Behavior: DecisionAllow},
		{Tool: "", Pattern: "rm -rf [", Behavior: DecisionDeny}, // a bad glob is fine as a command substring
	}}
	if err := good.Validate(); err != nil {
		t.Errorf("valid set rejected: %v", err)
	}
	var nilRS *RuleSet
	if err := nilRS.Validate(); err != nil {
		t.Errorf("nil set: %v", err)
	}

	bad := []struct {
		name string
		rs   *RuleSet
		want string
	}{
		{"typo behavior", &RuleSet{Rules: []Rule{{Tool: "x", Behavior: "dney"}}}, `unknown behavior "dney"`},
		{"empty behavior", &RuleSet{Rules: []Rule{{Tool: "x"}}}, "unknown behavior"},
		{"bad tool glob", &RuleSet{Rules: []Rule{{Tool: "[", Behavior: DecisionDeny}}}, "malformed tool glob"},
		{"bad mode", &RuleSet{Mode: "yollo"}, `unknown mode "yollo"`},
	}
	for _, tc := range bad {
		err := tc.rs.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.want)
		}
	}

	// Every problem is reported, not just the first.
	multi := &RuleSet{Mode: "bad", Rules: []Rule{{Tool: "[", Behavior: "dney"}}}
	if err := multi.Validate(); err == nil || strings.Count(err.Error(), "\n") < 2 {
		t.Errorf("expected several joined errors, got %v", err)
	}
}

func TestLoadRulesFromFileValidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.yaml")
	yml := "permissions:\n  rules:\n    - tool: shell\n      behavior: dney\n"
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadRulesFromFile(path)
	if err == nil || !strings.Contains(err.Error(), "unknown behavior") {
		t.Errorf("Load should reject a typo'd behavior, got %v", err)
	}
}

func TestMatcher(t *testing.T) {
	rs := &RuleSet{Rules: []Rule{{Tool: "*", Pattern: "/data/**", Behavior: DecisionDeny}}}
	ctx := context.Background()
	e := NewEngine(ModeDefault, rs)

	// Default keys: "paths" is not recognised, so the deny never fires.
	in := map[string]any{"paths": "/data/x"}
	if got := e.Check(ctx, "s", "t", in, ToolMeta{}); got.Decision != DecisionAllow {
		t.Errorf("unrecognised key should not match, got %s", got.Decision)
	}

	e = NewEngine(ModeDefault, rs, WithMatcher(Matcher{PathKeys: []string{"paths"}}))
	if got := e.Check(ctx, "s", "t", in, ToolMeta{}); got.Decision != DecisionDeny {
		t.Errorf("custom PathKeys should match, got %s", got.Decision)
	}
	// Custom PathKeys replace the defaults.
	if got := e.Check(ctx, "s", "t", map[string]any{"path": "/data/x"}, ToolMeta{}); got.Decision != DecisionAllow {
		t.Errorf("default key should no longer match, got %s", got.Decision)
	}

	// Custom command matcher: whole-word only.
	word := func(pattern, command string) bool {
		for _, f := range strings.Fields(command) {
			if f == pattern {
				return true
			}
		}
		return false
	}
	if !(&Matcher{}).matchInput("git", map[string]any{"command": "git; rm -rf ~"}) {
		t.Error("default substring match should (unsafely) match")
	}
	if (&Matcher{Command: word}).matchInput("git", map[string]any{"command": "git; rm -rf ~"}) {
		t.Error("custom command matcher should reject")
	}
	if !(&Matcher{Command: word}).matchInput("git", map[string]any{"command": "git status"}) {
		t.Error("custom command matcher should accept")
	}
	// Empty non-nil key slices disable that kind of matching.
	off := &Matcher{PathKeys: []string{}, CommandKeys: []string{}}
	if off.matchInput("x", map[string]any{"path": "x", "command": "x"}) {
		t.Error("disabled keys should never match")
	}
}
