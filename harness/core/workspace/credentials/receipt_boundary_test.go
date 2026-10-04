package credentials

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/harness/workspace/effects"
)

type boundarySink struct {
	rows []effects.Evidence
	hook func(context.Context, effects.Evidence) error
}

func (s *boundarySink) Record(ctx context.Context, e effects.Evidence) error {
	s.rows = append(s.rows, e.Clone())
	if s.hook != nil {
		return s.hook(ctx, e)
	}
	return nil
}
func TestReceiptBoundaryStopsBeforeCreate(t *testing.T) {
	for _, mode := range []string{"cancel", "authority", "custody", "source"} {
		for position := 1; position <= 3; position++ {
			t.Run(fmt.Sprintf("%s/%d", mode, position), func(t *testing.T) {
				g, c, f, _ := fixture()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				revoked := false
				c.Validate = func(context.Context) error {
					if revoked {
						return fixtureErr
					}
					return nil
				}
				p, pre := Preflight(ctx, g, c.PreflightContext, f)
				if pre.Outcome != effects.Prepared {
					t.Fatal(pre)
				}
				intents := 0
				c.Receipts = &boundarySink{hook: func(_ context.Context, e effects.Evidence) error {
					if e.Phase != effects.LinkIntentPhase {
						return nil
					}
					intents++
					if intents != position {
						return nil
					}
					switch mode {
					case "cancel":
						cancel()
					case "authority":
						revoked = true
					case "custody":
						f.failValidate = true
					case "source":
						f.sources[g.Bindings[position-1].Source] = false
					}
					return nil
				}}
				r := Apply(ctx, p, c, f)
				if f.creates != position-1 {
					t.Fatalf("creates after durable intent invalidated: got %d want %d; %+v", f.creates, position-1, r)
				}
				want := effects.Partial
				if position == 1 {
					want = effects.Conflict
				}
				if r.Outcome != want {
					t.Fatalf("got %+v want %s", r, want)
				}
			})
		}
	}
}
func TestReceiptCloseFailureRemainsPartial(t *testing.T) {
	g, c, f, _ := fixture()
	f.failClose = true
	s := &boundarySink{}
	c.Receipts = s
	r := apply(t, g, c, f)
	if r.Outcome != effects.Partial || r.Evidence.Outcome != effects.Partial || r.Evidence.Phase != effects.InterruptedPhase {
		t.Fatal(r)
	}
	row := s.rows[len(s.rows)-1]
	if row.Outcome != effects.Partial || row.Phase != effects.InterruptedPhase {
		t.Fatal(row)
	}
	if !hasCode(r, "recovery_required") || hasCode(r, "receipt_pending") {
		t.Fatal(r)
	}
}
func TestReceiptAbortReportsOnlyActualPersistenceFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			g, c, f, _ := fixture()
			f.failCreate = 1
			s := &boundarySink{hook: func(_ context.Context, e effects.Evidence) error {
				if fail && e.Phase == effects.AbortedPhase {
					return fixtureErr
				}
				return nil
			}}
			c.Receipts = s
			r := apply(t, g, c, f)
			if r.Outcome != effects.Conflict || r.Evidence.Phase != effects.AbortedPhase || hasCode(r, "receipt_pending") != fail || hasCode(r, "recovery_required") {
				t.Fatal(r)
			}
		})
	}
}
func TestReceiptCompensationHasNoSpuriousObligations(t *testing.T) {
	g, c, f, _ := fixture()
	f.failCreate = 2
	r := apply(t, g, c, f)
	if r.Outcome != effects.Partial || !hasCode(r, "recovery_required") || hasCode(r, "receipt_pending") || hasCode(r, "cleanup_deadline") {
		t.Fatal(r)
	}
	if f.removes != 1 || r.Evidence.Links[0].Outcome != effects.Removed {
		t.Fatal(r)
	}
}
func TestReceiptCleanupBudgetCannotExceedMaximum(t *testing.T) {
	g, c, f, _ := fixture()
	f.failClose = true
	c.CleanupTimeout = time.Hour
	observed := false
	c.Receipts = &boundarySink{hook: func(ctx context.Context, e effects.Evidence) error {
		if e.Phase != effects.InterruptedPhase {
			return nil
		}
		deadline, ok := ctx.Deadline()
		remaining := time.Until(deadline)
		if !ok || remaining <= 0 || remaining > DefaultCleanupTimeout {
			t.Errorf("cleanup budget: present=%v remaining=%v", ok, remaining)
		}
		observed = true
		return nil
	}}
	_ = apply(t, g, c, f)
	if !observed {
		t.Fatal("no interrupted receipt")
	}
}
func TestReceiptReplayEntriesStayAlreadyPresent(t *testing.T) {
	g, c, f, _ := fixture()
	first := apply(t, g, c, f)
	if first.Outcome != effects.Applied {
		t.Fatal(first)
	}
	r := apply(t, g, c, f)
	if r.Outcome != effects.AlreadyPresent {
		t.Fatal(r)
	}
	for _, entry := range r.Evidence.Links {
		if entry.Outcome != effects.AlreadyPresent || entry.Created || entry.Uncertain || entry.LinkIdentity == "" || entry.ParentIdentity == "" {
			t.Fatal(entry)
		}
	}
	inspected := Inspect(context.Background(), g, r.Evidence, c.PreflightContext, f)
	if inspected.Code != "inspected_complete" {
		t.Fatal(inspected)
	}
}
func TestReceiptInspectionRecognizesEveryInFlightPhase(t *testing.T) {
	for _, phase := range []effects.Phase{effects.IntentPhase, effects.LinkIntentPhase, effects.LinkCreatedPhase} {
		t.Run(string(phase), func(t *testing.T) {
			g, c, f, s := fixture()
			_ = apply(t, g, c, f)
			e := s.evidence[0].Clone()
			e.Phase = phase
			r := Inspect(context.Background(), g, e, c.PreflightContext, f)
			if r.Outcome == effects.Refused || !hasCode(r, "recovery_required") {
				t.Fatal(r)
			}
		})
	}
}
func TestReceiptInspectionRejectsInvalidAbortOutcome(t *testing.T) {
	for _, outcome := range []effects.Outcome{effects.Applied, effects.AlreadyPresent, effects.Pending, effects.Partial, effects.Prepared, effects.Unsupported, effects.Omitted, effects.Removed} {
		t.Run(string(outcome), func(t *testing.T) {
			g, c, f, _ := fixture()
			f.failCreate = 1
			r := apply(t, g, c, f)
			e := r.Evidence.Clone()
			e.Outcome = outcome
			inspected := Inspect(context.Background(), g, e, c.PreflightContext, f)
			if inspected.Outcome != effects.Refused || inspected.Code != "evidence_binding_refused" {
				t.Fatal(inspected)
			}
		})
	}
}
func TestReceiptInspectionRejectsIdentityOnUncreatedEntries(t *testing.T) {
	for _, mode := range []string{"pending-identity", "omitted-source", "omitted-target", "omitted-identity"} {
		t.Run(mode, func(t *testing.T) {
			g, c, f, s := fixture()
			if mode != "pending-identity" {
				g.Bindings[0].Required = false
				f.sources["a"] = false
			}
			_ = apply(t, g, c, f)
			e := s.evidence[0].Clone()
			switch mode {
			case "pending-identity":
				e.Links[0].LinkIdentity = "unproved"
			case "omitted-source":
				e.Links[0].Source = "unexpected"
			case "omitted-target":
				e.Links[0].Target = "unexpected"
			case "omitted-identity":
				e.Links[0].LinkIdentity = "unproved"
			}
			inspected := Inspect(context.Background(), g, e, c.PreflightContext, f)
			if inspected.Outcome != effects.Refused || inspected.Code != "evidence_binding_refused" {
				t.Fatal(inspected)
			}
		})
	}
}
func TestSourceWriteGrantAcceptedAndPreserved(t *testing.T) {
	g, c, f, _ := fixture()
	for n := range g.Bindings {
		g.Bindings[n].SourceWrite = true
		g.Bindings[n].SourceWriteAuthorizationID = "write-grant"
		g.Bindings[n].SourceWriteAuthorizationVersion = "write-version"
	}
	r := apply(t, g, c, f)
	if r.Outcome != effects.Applied || f.creates != len(g.Bindings) {
		t.Fatal(r)
	}
	for n, e := range r.Evidence.Links {
		if e.AuthorizationID != g.Bindings[n].AuthorizationID || e.AuthorizationVersion != g.Bindings[n].AuthorizationVersion {
			t.Fatal(e)
		}
	}
}
func TestSourceEscapeSignalRemainsTyped(t *testing.T) {
	for _, required := range []bool{false, true} {
		t.Run(fmt.Sprint(required), func(t *testing.T) {
			g, c, f, _ := fixture()
			g.Bindings[0].Required = required
			p := &viewPort{fakePort: f, source: func(ResolvedHome, string) (SourceObservation, error) { return SourceObservation{}, ErrSourceEscaped }}
			_, r := Preflight(context.Background(), g, c.PreflightContext, p)
			if r.Outcome != effects.Refused || r.Code != "source_escape" || f.creates != 0 {
				t.Fatal(r)
			}
		})
	}
}
func TestNilPortReturnsUnsupported(t *testing.T) {
	g, c, _, _ := fixture()
	_, r := Preflight(context.Background(), g, c.PreflightContext, nil)
	if r.Outcome != effects.Unsupported || r.Code != "platform_unsupported" {
		t.Fatal(r)
	}
}
func TestAllSourcesRecheckedBeforeFirstMutation(t *testing.T) {
	g, c, f, _ := fixture()
	calls := 0
	p := &viewPort{fakePort: f, source: func(h ResolvedHome, rel string) (SourceObservation, error) {
		calls++
		if calls == 9 {
			return SourceObservation{}, ErrSourceUnavailable
		}
		return f.Source(context.Background(), h, rel)
	}}
	prep, pre := Preflight(context.Background(), g, c.PreflightContext, p)
	if pre.Outcome != effects.Prepared {
		t.Fatal(pre)
	}
	r := Apply(context.Background(), prep, c, p)
	if r.Outcome != effects.Refused || r.Code != "source_unavailable" || f.creates != 0 {
		t.Fatal(r, f.creates)
	}
}
func TestReceiptRemovedEntriesRequireCreationEvidence(t *testing.T) {
	for _, mode := range []string{"complete", "not-created", "no-link-identity", "no-parent-identity"} {
		t.Run(mode, func(t *testing.T) {
			g, c, f, _ := fixture()
			f.failCreate = 2
			applied := apply(t, g, c, f)
			if applied.Evidence.Links[0].Outcome != effects.Removed {
				t.Fatal(applied)
			}
			e := applied.Evidence.Clone()
			switch mode {
			case "complete":
				e.Phase = effects.CompletePhase
				e.Outcome = effects.Applied
			case "not-created":
				e.Links[0].Created = false
			case "no-link-identity":
				e.Links[0].LinkIdentity = ""
			case "no-parent-identity":
				e.Links[0].ParentIdentity = ""
			}
			r := Inspect(context.Background(), g, e, c.PreflightContext, f)
			if r.Outcome != effects.Refused || r.Code != "evidence_binding_refused" {
				t.Fatal(r)
			}
		})
	}
}
func TestReceiptInspectUnavailableDistinguishesOwnedCreates(t *testing.T) {
	for _, created := range []bool{false, true} {
		for _, mode := range []string{"nil-port", "unsupported", "authority", "cancel"} {
			t.Run(fmt.Sprintf("created=%v/%s", created, mode), func(t *testing.T) {
				g, c, f, _ := fixture()
				r := apply(t, g, c, f)
				if !created {
					r = apply(t, g, c, f)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var port LinkPort = f
				switch mode {
				case "nil-port":
					port = nil
				case "unsupported":
					f.unsupported = true
				case "authority":
					c.Validate = func(context.Context) error { return fixtureErr }
				case "cancel":
					cancel()
				}
				got := Inspect(ctx, g, r.Evidence, c.PreflightContext, port)
				want := effects.Refused
				if mode == "nil-port" || mode == "unsupported" {
					want = effects.Unsupported
				}
				if created {
					want = effects.Partial
				}
				if got.Outcome != want || hasCode(got, "recovery_required") != created {
					t.Fatal(got)
				}
			})
		}
	}
}
func TestReceiptAbortCannotContainCreatedEntries(t *testing.T) {
	g, c, f, _ := fixture()
	r := apply(t, g, c, f)
	e := r.Evidence.Clone()
	e.Phase = effects.AbortedPhase
	e.Outcome = effects.Conflict
	got := Inspect(context.Background(), g, e, c.PreflightContext, f)
	if got.Outcome != effects.Refused || got.Code != "evidence_binding_refused" {
		t.Fatal(got)
	}
}

type erroredDestinationPort struct{ *fakePort }

func (p *erroredDestinationPort) Destination(ctx context.Context, r effects.RootInput, rel string) (LinkObservation, error) {
	o, _ := p.fakePort.Destination(ctx, r, rel)
	return o, fixtureErr
}
func TestReceiptInspectionNeverTrustsErroredObservation(t *testing.T) {
	g, c, f, _ := fixture()
	r := apply(t, g, c, f)
	got := Inspect(context.Background(), g, r.Evidence, c.PreflightContext, &erroredDestinationPort{f})
	if got.Outcome != effects.Partial || got.Code != "evidence_diverged" || !hasCode(got, "recovery_required") {
		t.Fatal(got)
	}
	for _, o := range got.Inspections {
		if o.State != effects.Divergent {
			t.Fatal(o)
		}
	}
}
func TestReceiptCompensatedEntriesInspectAsBefore(t *testing.T) {
	g, c, f, _ := fixture()
	f.failCreate = 2
	r := apply(t, g, c, f)
	got := Inspect(context.Background(), g, r.Evidence, c.PreflightContext, f)
	if got.Outcome != effects.Partial || got.Code != "inspected_interrupted" || !hasCode(got, "recovery_required") {
		t.Fatal(got)
	}
	for _, o := range got.Inspections {
		if o.State != effects.Before {
			t.Fatal(o)
		}
	}
}
func TestReceiptInspectionRequiresCurrentHostBinding(t *testing.T) {
	cases := map[string]func(*Group, *effects.PreflightContext){
		"inactive": func(g *Group, c *effects.PreflightContext) { g.Candidate.Inactive = false },
		"custody":  func(g *Group, c *effects.PreflightContext) { g.Candidate.PrivateCustody = false },
		"lock":     func(g *Group, c *effects.PreflightContext) { c.HeldLocks = nil },
		"layer":    func(g *Group, c *effects.PreflightContext) { g.Layer = InstalledLayer },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			g, c, f, _ := fixture()
			r := apply(t, g, c, f)
			change(&g, &c.PreflightContext)
			got := Inspect(context.Background(), g, r.Evidence, c.PreflightContext, f)
			if got.Outcome != effects.Refused {
				t.Fatal(got)
			}
		})
	}
}
func TestZeroPreparedGroupHasTypedRefusal(t *testing.T) {
	_, c, f, _ := fixture()
	r := Apply(context.Background(), PreparedGroup{}, c, f)
	if r.Outcome != effects.Refused || r.Code != "invalid_prepared_group" || f.creates != 0 {
		t.Fatal(r)
	}
}
