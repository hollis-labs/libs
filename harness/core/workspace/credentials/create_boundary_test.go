package credentials

import (
	"context"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/effects"
)

type createBoundaryPort struct {
	*fakePort
	mode                   string
	attempted, reinspected bool
}

func (p *createBoundaryPort) OpenCandidate(context.Context, effects.RootInput) (CandidateSession, error) {
	return &createBoundarySession{p}, nil
}

type createBoundarySession struct{ *createBoundaryPort }

func (s *createBoundarySession) CreateExclusive(ctx context.Context, source, rel string) (LinkObservation, error) {
	s.attempted = true
	if s.mode == "inspect-error" {
		s.creates++
		return LinkObservation{}, fixtureErr
	}
	o, e := s.fakePort.CreateExclusive(ctx, source, rel)
	if s.mode == "no-link-identity" {
		o.Identity = ""
	}
	if s.mode == "no-parent-identity" {
		o.ParentIdentity = ""
	}
	return o, e
}
func (s *createBoundarySession) Inspect(ctx context.Context, rel string) (LinkObservation, error) {
	if s.attempted {
		s.reinspected = true
		if s.mode == "inspect-error" {
			return LinkObservation{}, fixtureErr
		}
	}
	o, e := s.fakePort.Inspect(ctx, rel)
	if s.mode == "verification-error" && s.creates == 3 {
		return o, fixtureErr
	}
	return o, e
}
func TestUnprovedCreateAlwaysReobservedAndRetained(t *testing.T) {
	for _, mode := range []string{"no-link-identity", "no-parent-identity", "inspect-error"} {
		t.Run(mode, func(t *testing.T) {
			g, c, f, _ := fixture()
			p := &createBoundaryPort{fakePort: f, mode: mode}
			prep, pre := Preflight(context.Background(), g, c.PreflightContext, p)
			if pre.Outcome != effects.Prepared {
				t.Fatal(pre)
			}
			r := Apply(context.Background(), prep, c, p)
			if f.creates != 1 || !p.reinspected || r.Outcome != effects.Partial || !hasCode(r, "recovery_required") || !hasCode(r, "link_retained") {
				t.Fatal(r, p.reinspected)
			}
			if mode == "inspect-error" && (!r.Evidence.Links[0].Uncertain || r.Evidence.Links[0].Created || f.removes != 0) {
				t.Fatal(r)
			}
		})
	}
}
func TestLastDurableCreationStillRequiresAuthority(t *testing.T) {
	for _, mode := range []string{"cancel", "custody"} {
		t.Run(mode, func(t *testing.T) {
			g, c, f, _ := fixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			prep, pre := Preflight(ctx, g, c.PreflightContext, f)
			if pre.Outcome != effects.Prepared {
				t.Fatal(pre)
			}
			created := 0
			c.Receipts = &boundarySink{hook: func(_ context.Context, e effects.Evidence) error {
				if e.Phase != effects.LinkCreatedPhase {
					return nil
				}
				created++
				if created == len(g.Bindings) {
					if mode == "cancel" {
						cancel()
					} else {
						f.failValidate = true
					}
				}
				return nil
			}}
			r := Apply(ctx, prep, c, f)
			if r.Outcome != effects.Partial || !hasCode(r, "recovery_required") {
				t.Fatal(r)
			}
		})
	}
}

func TestFinalVerificationRejectsErroredMatchingObservation(t *testing.T) {
	g, c, f, _ := fixture()
	p := &createBoundaryPort{fakePort: f, mode: "verification-error"}
	prep, pre := Preflight(context.Background(), g, c.PreflightContext, p)
	if pre.Outcome != effects.Prepared {
		t.Fatal(pre)
	}
	r := Apply(context.Background(), prep, c, p)
	if r.Outcome != effects.Partial || r.Code != "verification_failed" || f.creates != 3 || f.removes != 3 || !hasCode(r, "recovery_required") {
		t.Fatal(r)
	}
}
