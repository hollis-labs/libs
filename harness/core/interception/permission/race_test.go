package permission

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestConcurrentEngine hammers every mutating and reading entry point at once.
// It is meaningful under -race; run it with -count=20.
func TestConcurrentEngine(t *testing.T) {
	rec := &recorder{}
	e := NewEngine(ModeDefault, &RuleSet{Rules: []Rule{{Tool: "shell", Pattern: "rm", Behavior: DecisionAsk}}},
		WithAuditor(rec), WithApprovalTimeout(2*time.Second), WithFileEditTools("edit"))
	ctx := context.Background()

	const workers = 8
	const iters = 50
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		session := fmt.Sprintf("s%d", w%3)
		wg.Add(4)
		go func() { // Check
			defer wg.Done()
			for i := 0; i < iters; i++ {
				e.Check(ctx, session, "shell", map[string]any{"command": "rm x"}, ToolMeta{IsDestructive: true})
			}
		}()
		go func() { // SetMode / SetRules / SetApprovalTimeout
			defer wg.Done()
			for i := 0; i < iters; i++ {
				e.SetMode([]Mode{ModeDefault, ModeAcceptEdits, ModePlan}[i%3])
				e.SetRules(&RuleSet{Rules: []Rule{{Tool: "shell", Behavior: DecisionAsk}}})
				e.SetApprovalTimeout(2 * time.Second)
				_ = e.Mode()
			}
		}()
		go func() { // ClearSessionGrants
			defer wg.Done()
			for i := 0; i < iters; i++ {
				e.ClearSessionGrants(session)
			}
		}()
		go func() { // Request / Respond / Wait, with a racing second Respond
			defer wg.Done()
			for i := 0; i < iters; i++ {
				req := e.RequestApproval(session, "shell", nil, "")
				var inner sync.WaitGroup
				inner.Add(2)
				for _, sc := range []Scope{ScopeSession, ScopeOnce} {
					go func() {
						defer inner.Done()
						e.Respond(req.ID, DecisionAllow, sc, session)
					}()
				}
				if resp := e.WaitForApproval(ctx, req); resp.TimedOut {
					t.Error("unexpected timeout")
				}
				inner.Wait()
			}
		}()
	}
	wg.Wait()
	if len(rec.snapshot()) == 0 {
		t.Error("auditor saw nothing")
	}
}
