package permission

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestEngineRespondWrongSession(t *testing.T) {
	e := NewEngine(ModeDefault, nil, WithApprovalTimeout(5*time.Second))
	req := e.RequestApproval("sess1", "bash", nil, "wrong session test")

	if e.Respond(req.ID, DecisionAllow, ScopeOnce, "sess2") {
		t.Error("Respond with a different session must return false")
	}

	// The request is still answerable by its own session.
	if !e.Respond(req.ID, DecisionDeny, ScopeOnce, "sess1") {
		t.Fatal("Respond by the owning session should succeed")
	}
	resp := e.WaitForApproval(context.Background(), req)
	if resp.Decision != DecisionDeny {
		t.Errorf("got %s, want deny", resp.Decision)
	}
}

func TestEngineRespondEmptySessionRejected(t *testing.T) {
	e := NewEngine(ModeDefault, nil, WithApprovalTimeout(5*time.Second))
	req := e.RequestApproval("sess1", "bash", nil, "empty claimed session")

	if e.Respond(req.ID, DecisionAllow, ScopeSession, "") {
		t.Fatal("an empty session id must not bypass the session check")
	}
	// No grant leaked from the rejected call.
	if got := e.Check(context.Background(), "sess1", "bash", nil, ToolMeta{IsDestructive: true}); got.Decision != DecisionAsk {
		t.Errorf("rejected Respond leaked a grant: %s", got.Decision)
	}
}

func TestEngineRespondTwice(t *testing.T) {
	e := NewEngine(ModeDefault, nil, WithApprovalTimeout(5*time.Second))
	req := e.RequestApproval("sess1", "bash", nil, "double answer")

	if !e.Respond(req.ID, DecisionDeny, ScopeOnce, "sess1") {
		t.Fatal("first Respond should succeed")
	}
	// A second answer must neither succeed nor record a grant.
	if e.Respond(req.ID, DecisionAllow, ScopeSession, "sess1") {
		t.Error("second Respond should return false")
	}
	e.WaitForApproval(context.Background(), req)
	if got := e.Check(context.Background(), "sess1", "bash", nil, ToolMeta{IsDestructive: true}); got.Decision != DecisionAsk {
		t.Errorf("second Respond leaked a grant: %s", got.Decision)
	}
}

func TestEngineClearSessionGrants(t *testing.T) {
	e := NewEngine(ModeDefault, nil, WithApprovalTimeout(time.Second))
	req := e.RequestApproval("sess1", "bash", nil, "grant")
	e.Respond(req.ID, DecisionAllow, ScopeSession, "sess1")
	e.WaitForApproval(context.Background(), req)

	ctx := context.Background()
	if got := e.Check(ctx, "sess1", "bash", nil, ToolMeta{IsDestructive: true}); got.Decision != DecisionAllow {
		t.Fatalf("grant not active: %s", got.Decision)
	}
	// Another session is unaffected by, and does not see, the grant.
	if got := e.Check(ctx, "sess2", "bash", nil, ToolMeta{IsDestructive: true}); got.Decision != DecisionAsk {
		t.Errorf("grant leaked across sessions: %s", got.Decision)
	}

	e.ClearSessionGrants("sess1")
	if got := e.Check(ctx, "sess1", "bash", nil, ToolMeta{IsDestructive: true}); got.Decision != DecisionAsk {
		t.Errorf("grant survived ClearSessionGrants: %s", got.Decision)
	}
}

func TestEngineSetRules(t *testing.T) {
	e := NewEngine(ModeDefault, nil)
	e.SetRules(&RuleSet{Rules: []Rule{{Tool: "bash", Behavior: DecisionDeny}}})

	result := e.Check(context.Background(), "sess1", "bash", nil, ToolMeta{IsReadOnly: true})
	if result.Decision != DecisionDeny {
		t.Errorf("got %s, want deny", result.Decision)
	}

	e.SetRules(nil) // nil rules are tolerated
	result = e.Check(context.Background(), "sess1", "bash", nil, ToolMeta{IsReadOnly: true})
	if result.Decision != DecisionAllow {
		t.Errorf("got %s after SetRules(nil), want allow", result.Decision)
	}
}

func TestEngineSetModeThreadSafe(t *testing.T) {
	e := NewEngine(ModeDefault, nil)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			e.SetMode(ModeYolo)
		}()
		go func() {
			defer wg.Done()
			_ = e.Mode()
		}()
	}
	wg.Wait()
	if e.Mode() != ModeYolo {
		t.Errorf("mode = %s, want yolo", e.Mode())
	}
}

func TestNewEngineDefaults(t *testing.T) {
	e := NewEngine(ModeDefault, nil)
	if e.approvalTimeout != 5*time.Minute {
		t.Errorf("default approval timeout = %v, want 5m", e.approvalTimeout)
	}
	e = NewEngine(ModeDefault, nil, WithApprovalTimeout(time.Second), nil)
	if e.approvalTimeout != time.Second {
		t.Errorf("WithApprovalTimeout not applied: %v", e.approvalTimeout)
	}
}

func TestWithFileEditTools(t *testing.T) {
	ctx := context.Background()
	// Default set is empty: a named edit tool is just a non-destructive write.
	plain := NewEngine(ModeAcceptEdits, nil)
	if got := plain.Check(ctx, "s", "edit_file", nil, ToolMeta{}); got.Decision != DecisionAsk {
		t.Errorf("no registered edit tools: got %s, want ask", got.Decision)
	}
	e := NewEngine(ModeAcceptEdits, nil, WithFileEditTools("edit_file"))
	if got := e.Check(ctx, "s", "edit_file", nil, ToolMeta{}); got.Decision != DecisionAllow {
		t.Errorf("registered edit tool: got %s, want allow", got.Decision)
	}
	// ToolMeta.IsFileEdit works without registration.
	if got := plain.Check(ctx, "s", "anything", nil, ToolMeta{IsFileEdit: true}); got.Decision != DecisionAllow {
		t.Errorf("IsFileEdit: got %s, want allow", got.Decision)
	}
}

func TestRequestIDsAreRandomHex(t *testing.T) {
	e := NewEngine(ModeDefault, nil)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := e.RequestApproval("s", "t", nil, "").ID
		if len(id) != 32 {
			t.Fatalf("id %q: want 32 hex chars", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestRespondProjectScope(t *testing.T) {
	ctx := context.Background()
	run := func(t *testing.T, e *Engine, d Decision) ApprovalResponse {
		t.Helper()
		req := e.RequestApproval("s1", "bash", nil, "r")
		if !e.Respond(req.ID, d, ScopeProject, "s1") {
			t.Fatal("Respond returned false")
		}
		return e.WaitForApproval(ctx, req)
	}

	t.Run("no store behaves as once", func(t *testing.T) {
		e := NewEngine(ModeDefault, nil, WithApprovalTimeout(time.Second))
		if got := run(t, e, DecisionAllow); got.Scope != ScopeOnce {
			t.Errorf("scope = %s, want once", got.Scope)
		}
	})
	t.Run("store receives an allow", func(t *testing.T) {
		st := &fakeStore{}
		e := NewEngine(ModeDefault, nil, WithApprovalTimeout(time.Second), WithRuleStore(st))
		if got := run(t, e, DecisionAllow); got.Scope != ScopeProject {
			t.Errorf("scope = %s, want project", got.Scope)
		}
		if len(st.rules) != 1 || st.rules[0].Tool != "bash" || st.rules[0].Behavior != DecisionAllow || st.sessions[0] != "s1" {
			t.Errorf("store got %+v", st.rules)
		}
	})
	t.Run("deny is not persisted", func(t *testing.T) {
		st := &fakeStore{}
		e := NewEngine(ModeDefault, nil, WithApprovalTimeout(time.Second), WithRuleStore(st))
		got := run(t, e, DecisionDeny)
		if got.Scope != ScopeOnce || len(st.rules) != 0 {
			t.Errorf("scope=%s rules=%v", got.Scope, st.rules)
		}
	})
	t.Run("append error downgrades to once and is audited", func(t *testing.T) {
		st := &fakeStore{err: context.DeadlineExceeded}
		rec := &recorder{}
		e := NewEngine(ModeDefault, nil, WithApprovalTimeout(time.Second), WithRuleStore(st), WithAuditor(rec))
		if got := run(t, e, DecisionAllow); got.Decision != DecisionAllow || got.Scope != ScopeOnce {
			t.Errorf("got %+v, want allow/once", got)
		}
		var found bool
		for _, ev := range rec.snapshot() {
			if ev.Kind == EventGrantRecorded && ev.Result.Reason != "" {
				found = true
			}
		}
		if !found {
			t.Error("no grant_recorded event describing the store failure")
		}
	})
}

type fakeStore struct {
	rules    []Rule
	sessions []string
	err      error
}

func (f *fakeStore) Append(_ context.Context, _ Scope, sessionID string, r Rule) error {
	if f.err != nil {
		return f.err
	}
	f.rules = append(f.rules, r)
	f.sessions = append(f.sessions, sessionID)
	return nil
}
