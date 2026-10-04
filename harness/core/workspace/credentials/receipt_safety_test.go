package credentials

import (
	"context"
	"fmt"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"testing"
	"time"
)

func TestPreparedAndPendingEvidence(t *testing.T) {
	g, c, f, s := fixture()
	prep, pre := Preflight(context.Background(), g, c.PreflightContext, f)
	if pre.Outcome != effects.Outcome("prepared") {
		t.Errorf("preflight claims %s", pre.Outcome)
	}
	got := Apply(context.Background(), prep, c, f)
	if got.Outcome != effects.Applied {
		t.Fatal(got)
	}
	for _, e := range s.evidence {
		if e.Phase != effects.CompletePhase && e.Outcome != effects.Pending {
			t.Errorf("in-flight %s claims %s", e.Phase, e.Outcome)
		}
	}
	e := s.evidence[0].Clone()
	e.Outcome = effects.AlreadyPresent
	if got := Inspect(context.Background(), g, e, c.PreflightContext, f); got.Outcome != effects.Refused {
		t.Fatal("accepted in-flight already-present", got)
	}
}
func TestFailureBeforeMutationClosesIntent(t *testing.T) {
	g, c, f, s := fixture()
	s.hook = func(n int) {
		if n == 1 {
			f.sources["a"] = false
		}
	}
	got := apply(t, g, c, f)
	last := s.evidence[len(s.evidence)-1]
	if f.creates != 0 || last.Phase != effects.Phase("aborted_before_mutation") {
		t.Fatal("missing terminal abort", got, last)
	}
	inspected := Inspect(context.Background(), g, last, c.PreflightContext, f)
	if len(inspected.Obligations) != 0 || inspected.Outcome != effects.Conflict {
		t.Fatal(inspected)
	}
}
func TestPostMutationDivergenceIsPartial(t *testing.T) {
	g, c, f, _ := fixture()
	got := apply(t, g, c, f)
	v := f.links["a"]
	v.Identity = "replacement"
	f.links["a"] = v
	inspected := Inspect(context.Background(), g, got.Evidence, c.PreflightContext, f)
	if inspected.Outcome != effects.Partial || !hasCode(inspected, "recovery_required") {
		t.Fatal(inspected)
	}
}

type deadlineSink struct {
	records       int
	cleanupBudget time.Duration
}

func (s *deadlineSink) Record(ctx context.Context, e effects.Evidence) error {
	s.records++
	if e.Phase == effects.InterruptedPhase {
		deadline, ok := ctx.Deadline()
		if !ok {
			return fixtureErr
		}
		s.cleanupBudget = time.Until(deadline)
		return nil
	}
	return nil
}
func TestCleanupHasBoundedDeadline(t *testing.T) {
	g, c, f, _ := fixture()
	f.failCreate = 2
	s := &deadlineSink{}
	c.Receipts = s
	got := apply(t, g, c, f)
	if got.Outcome != effects.Partial || s.cleanupBudget <= 0 || s.cleanupBudget > 5*time.Second {
		t.Fatal("cleanup has no bounded deadline", got, s.cleanupBudget)
	}
}

func TestCleanupDeadlineRetainsAndReturns(t *testing.T) {
	g, c, f, _ := fixture()
	c.CleanupTimeout = 10 * time.Millisecond
	f.failCreate = 2
	c.Validate = func(ctx context.Context) error {
		if f.creates < 2 {
			return nil
		}
		if _, ok := ctx.Deadline(); !ok {
			return fixtureErr
		}
		<-ctx.Done()
		return ctx.Err()
	}
	start := time.Now()
	got := apply(t, g, c, f)
	if got.Outcome != effects.Partial || !f.links["a"].Exists || !hasCode(got, "link_retained") || !hasCode(got, "cleanup_deadline") || time.Since(start) > time.Second {
		t.Fatal(got)
	}
}

type expiringSink struct{}

func (*expiringSink) Record(ctx context.Context, e effects.Evidence) error {
	if e.Phase == effects.InterruptedPhase {
		if _, ok := ctx.Deadline(); !ok {
			return fixtureErr
		}
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}
func TestCleanupReceiptDeadlineReported(t *testing.T) {
	g, c, f, _ := fixture()
	f.failCreate = 2
	c.CleanupTimeout = 10 * time.Millisecond
	c.Receipts = &expiringSink{}
	got := apply(t, g, c, f)
	if got.Outcome != effects.Partial || !hasCode(got, "receipt_pending") || !hasCode(got, "cleanup_deadline") {
		t.Fatal(got)
	}
}
func TestBindingBound(t *testing.T) {
	for _, count := range []int{MaxBindings, MaxBindings + 1} {
		g, c, f, _ := fixture()
		g.Bindings = nil
		for n := 0; n < count; n++ {
			name := fmt.Sprintf("resource%03d", n)
			g.Bindings = append(g.Bindings, Binding{Source: name, Destination: name, Required: true, AuthorizationID: "grant", AuthorizationVersion: "version", SourceRead: true})
			f.sources[name] = true
		}
		prep, pre := Preflight(context.Background(), g, c.PreflightContext, f)
		if count > MaxBindings {
			if pre.Outcome != effects.Refused {
				t.Fatal(pre)
			}
			continue
		}
		if pre.Outcome != effects.Prepared {
			t.Fatal(pre)
		}
		got := Apply(context.Background(), prep, c, f)
		if got.Outcome != effects.Applied || f.creates != count {
			t.Fatal(got)
		}
	}
}
