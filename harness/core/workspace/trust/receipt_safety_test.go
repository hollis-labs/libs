package trust

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/harness/workspace/effects"
)

type recordingSink struct {
	rows []effects.Evidence
	hook func(context.Context, effects.Evidence) error
}

func (s *recordingSink) Record(ctx context.Context, e effects.Evidence) error {
	s.rows = append(s.rows, e.Clone())
	if s.hook != nil {
		return s.hook(ctx, e)
	}
	return nil
}

type observedPort struct {
	*fakePort
	begins      int
	unavailable bool
}

func (p *observedPort) Begin(context.Context, Request) (Session, error) {
	p.begins++
	if p.unavailable {
		return nil, errors.New("fixture lock unavailable")
	}
	return p.fakePort, nil
}
func hasObligation(r effects.Result, code string) bool {
	for _, o := range r.Obligations {
		if o.Code == code {
			return true
		}
	}
	return false
}
func TestPreflightAndIntentArePreparedAndPending(t *testing.T) {
	r, c := fixture()
	p := &fakePort{}
	s := &recordingSink{}
	c.Receipts = s
	prepared, pre := Preflight(context.Background(), r, c.PreflightContext, p)
	if pre.Outcome != effects.Prepared || pre.Evidence.Trust[0].Outcome != effects.Prepared {
		t.Fatal(pre)
	}
	got := Apply(context.Background(), prepared, c, p)
	if got.Outcome != effects.Applied {
		t.Fatal(got)
	}
	if len(s.rows) < 2 || s.rows[0].Phase != effects.IntentPhase || s.rows[0].Outcome != effects.Pending || s.rows[0].Trust[0].Outcome != effects.Pending {
		t.Fatal(s.rows)
	}
}
func TestReceiptCallbackRevocationPreventsLockAcquisition(t *testing.T) {
	for _, mode := range []string{"cancel", "authority"} {
		t.Run(mode, func(t *testing.T) {
			r, c := fixture()
			p := &observedPort{fakePort: &fakePort{}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			revoked := false
			c.Validate = func(context.Context) error {
				if revoked {
					return errors.New("fixture revoked")
				}
				return nil
			}
			prepared, pre := Preflight(ctx, r, c.PreflightContext, p)
			if pre.Code != "preflight_complete" {
				t.Fatal(pre)
			}
			c.Receipts = &recordingSink{hook: func(_ context.Context, e effects.Evidence) error {
				if e.Phase == effects.IntentPhase {
					if mode == "cancel" {
						cancel()
					} else {
						revoked = true
					}
				}
				return nil
			}}
			got := Apply(ctx, prepared, c, p)
			if p.begins != 0 || p.updates != 0 || got.Outcome != effects.Refused || got.Evidence.Phase != effects.AbortedPhase {
				t.Fatal(got, p.begins, p.updates)
			}
		})
	}
}
func TestLockFailureHasTerminalAbort(t *testing.T) {
	r, c := fixture()
	p := &observedPort{fakePort: &fakePort{}, unavailable: true}
	s := &recordingSink{}
	c.Receipts = s
	prepared, _ := Preflight(context.Background(), r, c.PreflightContext, p)
	got := Apply(context.Background(), prepared, c, p)
	if got.Outcome != effects.Conflict || got.Evidence.Phase != effects.AbortedPhase || got.Evidence.Trust[0].Outcome != effects.Conflict {
		t.Fatal(got)
	}
	if len(s.rows) < 2 || s.rows[len(s.rows)-1].Phase != effects.AbortedPhase {
		t.Fatal(s.rows)
	}
	inspected := Inspect(context.Background(), prepared, c.PreflightContext, p, got.Evidence)
	if inspected.Outcome != effects.Conflict || hasObligation(inspected, "recovery_required") {
		t.Fatal(inspected)
	}
}
func TestCleanupReceiptHasMaximumDeadline(t *testing.T) {
	for _, budget := range []time.Duration{0, time.Hour, 10 * time.Millisecond} {
		t.Run(budget.String(), func(t *testing.T) {
			r, c := fixture()
			c.CleanupTimeout = budget
			p := &fakePort{closeErr: errors.New("fixture close failed")}
			seen := false
			c.Receipts = &recordingSink{hook: func(ctx context.Context, e effects.Evidence) error {
				if e.Phase != effects.InterruptedPhase {
					return nil
				}
				deadline, ok := ctx.Deadline()
				remaining := time.Until(deadline)
				cap := 5 * time.Second
				if budget > 0 && budget < cap {
					cap = budget
				}
				if !ok || remaining <= 0 || remaining > cap {
					t.Errorf("deadline=%v remaining=%v max=%v", ok, remaining, cap)
				}
				seen = true
				return nil
			}}
			prepared, _ := Preflight(context.Background(), r, c.PreflightContext, p)
			got := Apply(context.Background(), prepared, c, p)
			if !seen || got.Outcome != effects.Partial || !hasObligation(got, "recovery_required") {
				t.Fatal(got)
			}
		})
	}
}
func TestExpiredReceiptCleanupRetainsObligations(t *testing.T) {
	r, c := fixture()
	c.CleanupTimeout = 10 * time.Millisecond
	p := &fakePort{closeErr: errors.New("fixture close failed")}
	c.Receipts = &recordingSink{hook: func(ctx context.Context, e effects.Evidence) error {
		if e.Phase != effects.InterruptedPhase {
			return nil
		}
		if _, ok := ctx.Deadline(); !ok {
			return errors.New("fixture missing deadline")
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	prepared, _ := Preflight(context.Background(), r, c.PreflightContext, p)
	started := time.Now()
	got := Apply(context.Background(), prepared, c, p)
	if got.Outcome != effects.Partial || !hasObligation(got, "recovery_required") || !hasObligation(got, "receipt_pending") || !hasObligation(got, "cleanup_deadline") || time.Since(started) > time.Second {
		t.Fatal(got)
	}
}
func TestInspectionKeepsAppliedDivergencePartial(t *testing.T) {
	r, c := fixture()
	p := &fakePort{}
	prepared, _ := Preflight(context.Background(), r, c.PreflightContext, p)
	got := Apply(context.Background(), prepared, c, p)
	p.present = false
	inspected := Inspect(context.Background(), prepared, c.PreflightContext, p, got.Evidence)
	if inspected.Outcome != effects.Partial || !hasObligation(inspected, "recovery_required") || inspected.Inspections[0].State != effects.Missing {
		t.Fatal(inspected)
	}
}
func TestOptionalClaudeUnsupportedCanInspectOmission(t *testing.T) {
	r, c := fixture()
	r.Required = false
	prepared, _ := Preflight(context.Background(), r, c.PreflightContext, nil)
	got := Apply(context.Background(), prepared, c, nil)
	if got.Outcome != effects.Omitted {
		t.Fatal(got)
	}
	inspected := Inspect(context.Background(), prepared, c.PreflightContext, nil, got.Evidence)
	if inspected.Outcome != effects.Omitted {
		t.Fatal(inspected)
	}
}
func TestLockNamespacesOutsideBothDeclaredRoots(t *testing.T) {
	for _, configLock := range []bool{false, true} {
		t.Run(map[bool]string{false: "runtime-lock-in-config", true: "config-lock-in-runtime"}[configLock], func(t *testing.T) {
			r, c := fixture()
			if configLock {
				c.HeldLocks[0].Namespace = r.Target.CanonicalParent + "/locks"
			} else {
				c.HeldLocks[1].Namespace = r.Config.Path + "/locks"
			}
			_, pre := Preflight(context.Background(), r, c.PreflightContext, &fakePort{})
			if pre.Outcome != effects.Refused {
				t.Fatal(pre)
			}
		})
	}
}
func TestIntentCannotClaimAlreadyPresent(t *testing.T) {
	r, c := fixture()
	p := &fakePort{present: true}
	prepared, _ := Preflight(context.Background(), r, c.PreflightContext, p)
	got := Apply(context.Background(), prepared, c, p)
	e := got.Evidence.Clone()
	e.Phase = effects.IntentPhase
	inspected := Inspect(context.Background(), prepared, c.PreflightContext, p, e)
	if inspected.Outcome != effects.Refused {
		t.Fatal(inspected)
	}
}
func TestAlreadyPresentDivergenceRemainsConflict(t *testing.T) {
	r, c := fixture()
	p := &fakePort{present: true}
	prepared, _ := Preflight(context.Background(), r, c.PreflightContext, p)
	got := Apply(context.Background(), prepared, c, p)
	p.present = false
	inspected := Inspect(context.Background(), prepared, c.PreflightContext, p, got.Evidence)
	if inspected.Outcome != effects.Conflict || !hasObligation(inspected, "recovery_required") {
		t.Fatal(inspected)
	}
}
