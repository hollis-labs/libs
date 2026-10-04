package credentials

import (
	"context"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/effects"
)

// A port whose CreateExclusive creates the link but reports a bare error with no observation.
type silentCreatePort struct{ *fakePort }

func (s *silentCreatePort) OpenCandidate(context.Context, effects.RootInput) (CandidateSession, error) {
	return s, nil
}
func (s *silentCreatePort) CreateExclusive(ctx context.Context, source, p string) (LinkObservation, error) {
	_, _ = s.fakePort.CreateExclusive(ctx, source, p) // link IS created in fakePort.links
	return LinkObservation{}, fixtureErr              // ... but port reports nothing
}

func TestSafetyCreateErrorWithoutObservationIsTreatedAsNoMutation(t *testing.T) {
	g, c, f, _ := fixture()
	sp := &silentCreatePort{fakePort: f}
	p, pre := Preflight(context.Background(), g, c.PreflightContext, sp)
	if pre.Code != "preflight_complete" {
		t.Fatal(pre)
	}
	r := Apply(context.Background(), p, c, sp)
	t.Logf("outcome=%s code=%s links-on-port=%d obligations=%v", r.Outcome, r.Code, len(f.links), r.Obligations)
	if len(f.links) > 0 && r.Outcome != effects.Partial {
		t.Errorf("a link exists after the call but the result is %s (not Partial) and no obligation names it", r.Outcome)
	}
}

func TestSafetyNormalizationAliasAccepted(t *testing.T) {
	g, c, _, _ := fixture()
	g.Bindings = []Binding{
		{Source: "a", Destination: "café", Required: true, AuthorizationID: "g", AuthorizationVersion: "v", SourceRead: true},
		{Source: "b", Destination: "café", Required: true, AuthorizationID: "g", AuthorizationVersion: "v", SourceRead: true},
	}
	g = freeze(g)
	if binding(g, c.PreflightContext) == "" {
		t.Fatal("Unicode destinations accepted")
	}
}
