package permission

import (
	"context"
	"testing"
	"time"
)

// These tests pin the current evaluation order of Engine.Check:
//
//	yolo > plan > session grant > deny > ask > allow rules > mode default
//
// They are characterization tests. Two results are surprising and
// deliberate: yolo beats deny rules, and a session grant (keyed by tool
// NAME only) beats deny rules. Changing either is a behavior change.

func grantSession(t *testing.T, e *Engine, session, tool string) {
	t.Helper()
	req := e.RequestApproval(session, tool, nil, "grant")
	if !e.Respond(req.ID, DecisionAllow, ScopeSession, session) {
		t.Fatal("Respond failed")
	}
	e.WaitForApproval(context.Background(), req)
}

func TestPrecedence_YoloBeatsDenyRule(t *testing.T) {
	rules := &RuleSet{Rules: []Rule{{Tool: "shell", Behavior: DecisionDeny}}}
	e := NewEngine(ModeYolo, rules)
	got := e.Check(context.Background(), "s", "shell", nil, ToolMeta{IsDestructive: true})
	if got.Decision != DecisionAllow || got.MatchedRule != nil {
		t.Errorf("yolo must allow past a deny rule, got %+v", got)
	}
}

func TestPrecedence_PlanBeatsSessionGrantAndAllowRule(t *testing.T) {
	rules := &RuleSet{Rules: []Rule{{Tool: "shell", Behavior: DecisionAllow}}}
	e := NewEngine(ModeDefault, rules, WithApprovalTimeout(time.Second))
	grantSession(t, e, "s", "shell")
	e.SetMode(ModePlan)

	got := e.Check(context.Background(), "s", "shell", nil, ToolMeta{IsReadOnly: false})
	if got.Decision != DecisionDeny {
		t.Errorf("plan mode must deny writes despite grant and allow rule, got %+v", got)
	}
	// And plan mode allows a read-only tool even when a deny rule names it.
	rules.Rules = append(rules.Rules, Rule{Tool: "reader", Behavior: DecisionDeny})
	got = e.Check(context.Background(), "s", "reader", nil, ToolMeta{IsReadOnly: true})
	if got.Decision != DecisionAllow {
		t.Errorf("plan mode read-only should skip rules, got %+v", got)
	}
}

func TestPrecedence_SessionGrantBeatsDenyRule(t *testing.T) {
	rules := &RuleSet{Rules: []Rule{{Tool: "shell", Pattern: "rm -rf", Behavior: DecisionDeny}}}
	e := NewEngine(ModeDefault, rules, WithApprovalTimeout(time.Second))
	in := map[string]any{"command": "rm -rf /"}
	ctx := context.Background()

	if got := e.Check(ctx, "s", "shell", in, ToolMeta{}); got.Decision != DecisionDeny {
		t.Fatalf("baseline should deny, got %+v", got)
	}
	// "Allow for session" on the tool name (for any input) overrides the
	// pattern-scoped deny rule, including for a different, dangerous input.
	grantSession(t, e, "s", "shell")
	got := e.Check(ctx, "s", "shell", in, ToolMeta{})
	if got.Decision != DecisionAllow || got.Reason != "session grant" {
		t.Errorf("session grant must beat deny rules, got %+v", got)
	}
	// The grant is per session.
	if got := e.Check(ctx, "other", "shell", in, ToolMeta{}); got.Decision != DecisionDeny {
		t.Errorf("grant must not leak to another session, got %+v", got)
	}
}

func TestPrecedence_DenyBeatsAskBeatsAllowRules(t *testing.T) {
	ctx := context.Background()
	all := []Rule{
		{Tool: "t", Behavior: DecisionAllow},
		{Tool: "t", Behavior: DecisionAsk},
		{Tool: "t", Behavior: DecisionDeny},
	}
	steps := []struct {
		rules []Rule
		want  Decision
	}{
		{all, DecisionDeny},
		{all[:2], DecisionAsk},
		{all[:1], DecisionAllow},
	}
	for _, s := range steps {
		e := NewEngine(ModeDefault, &RuleSet{Rules: s.rules})
		if got := e.Check(ctx, "s", "t", nil, ToolMeta{IsDestructive: true}); got.Decision != s.want {
			t.Errorf("%d rules: got %s, want %s", len(s.rules), got.Decision, s.want)
		}
	}
}

func TestPrecedence_AllowRuleBeatsModeDefault(t *testing.T) {
	// Mode default alone would ask for a destructive tool; an allow rule wins.
	rules := &RuleSet{Rules: []Rule{{Tool: "shell", Behavior: DecisionAllow}}}
	e := NewEngine(ModeDefault, rules)
	got := e.Check(context.Background(), "s", "shell", nil, ToolMeta{IsDestructive: true})
	if got.Decision != DecisionAllow || got.MatchedRule == nil {
		t.Errorf("allow rule must beat the mode default, got %+v", got)
	}
}

func TestPrecedence_ModeDefaultWhenNothingMatches(t *testing.T) {
	rules := &RuleSet{Rules: []Rule{{Tool: "other", Behavior: DecisionDeny}}}
	e := NewEngine(ModeDefault, rules)
	got := e.Check(context.Background(), "s", "shell", nil, ToolMeta{IsDestructive: true})
	if got.Decision != DecisionAsk || got.MatchedRule != nil {
		t.Errorf("mode default expected, got %+v", got)
	}
}
