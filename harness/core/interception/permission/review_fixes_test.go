package permission

import (
	"context"
	"sync"
	"testing"
	"time"
)

// A Respond that lands while the waiter is timing out must never leave a grant
// behind that contradicts what the waiter told its caller: either the wait
// returns the allow and the grant exists, or the wait returns a deny/timeout,
// Respond returns false and no grant exists. Never a mix.
func TestLateRespondNeverContradictsTheWaiter(t *testing.T) {
	ctx := context.Background()
	for i := 0; i < 300; i++ {
		e := NewEngine(ModeDefault, nil, WithApprovalTimeout(time.Millisecond))
		req := e.RequestApproval("s1", "bash", nil, "race")

		var wg sync.WaitGroup
		var responded bool
		var resp ApprovalResponse
		wg.Add(2)
		go func() { defer wg.Done(); resp = e.WaitForApproval(ctx, req) }()
		go func() {
			defer wg.Done()
			time.Sleep(time.Millisecond) // land around the timer
			responded = e.Respond(req.ID, DecisionAllow, ScopeSession, "s1")
		}()
		wg.Wait()

		granted := e.Check(ctx, "s1", "bash", nil, ToolMeta{IsDestructive: true}).Decision == DecisionAllow
		switch {
		case responded && resp.Decision == DecisionAllow && granted:
			// the answer won: allow, and the grant it recorded exists
		case !responded && resp.Decision == DecisionDeny && !granted:
			// the waiter won: deny/timeout, Respond refused, nothing recorded
		default:
			t.Fatalf("iteration %d contradicts itself: responded=%v wait=%s timedOut=%v granted=%v",
				i, responded, resp.Decision, resp.TimedOut, granted)
		}
	}
}

func TestCancelledWaitClaimsTheRequest(t *testing.T) {
	e := NewEngine(ModeDefault, nil, WithApprovalTimeout(time.Minute))
	req := e.RequestApproval("s1", "bash", nil, "cancel")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := e.WaitForApproval(ctx, req); got.Decision != DecisionDeny {
		t.Fatalf("canceled wait = %s, want deny", got.Decision)
	}
	if e.Respond(req.ID, DecisionAllow, ScopeSession, "s1") {
		t.Error("Respond after a canceled wait must return false")
	}
	if got := e.Check(context.Background(), "s1", "bash", nil, ToolMeta{IsDestructive: true}); got.Decision == DecisionAllow {
		t.Error("a Respond after cancellation recorded a grant")
	}
}

func TestRespondRejectsNonAnswers(t *testing.T) {
	for _, d := range []Decision{"", DecisionAsk, "dney", "ALLOW"} {
		e := NewEngine(ModeDefault, nil, WithApprovalTimeout(time.Minute))
		req := e.RequestApproval("s1", "bash", nil, "bad decision")
		if e.Respond(req.ID, d, ScopeSession, "s1") {
			t.Errorf("Respond(%q) returned true", d)
		}
		// The request is still open: a real answer still works.
		if !e.Respond(req.ID, DecisionAllow, ScopeOnce, "s1") {
			t.Errorf("a rejected Respond(%q) used up the request", d)
		}
	}
}

// ".." must not step out from under an allow rule or around a deny rule.
func TestInputPathsAreCleanedBeforeMatching(t *testing.T) {
	deny := &RuleSet{Rules: []Rule{{Tool: "read", Pattern: "/etc/**", Behavior: DecisionDeny}}}
	allow := &RuleSet{Rules: []Rule{{Tool: "read", Pattern: "/work/**", Behavior: DecisionAllow}}}
	ctx := context.Background()
	meta := ToolMeta{IsDestructive: true}
	check := func(rs *RuleSet, path string) Decision {
		return NewEngine(ModeDefault, rs).Check(ctx, "s", "read", map[string]any{"path": path}, meta).Decision
	}

	if got := check(deny, "/tmp/../etc/passwd"); got != DecisionDeny {
		t.Errorf("deny /etc/** vs /tmp/../etc/passwd = %s, want deny", got)
	}
	if got := check(allow, "/work/../etc/passwd"); got == DecisionAllow {
		t.Errorf("allow /work/** matched /work/../etc/passwd")
	}
	if got := check(allow, "/work/proj/a/b"); got != DecisionAllow {
		t.Errorf("allow /work/** vs /work/proj/a/b = %s, want allow", got)
	}
	if got := check(allow, "/work/proj/../proj/a"); got != DecisionAllow {
		t.Errorf("an in-tree .. must still match: %s", got)
	}
	// Lexical only: a segment that merely looks like a sibling is not the tree.
	if got := check(allow, "/work-secret/x"); got == DecisionAllow {
		t.Error("/work-secret/x matched /work/**")
	}
}
