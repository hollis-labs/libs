package credentials

import (
	"context"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/effects"
)

// Apply must refuse on its own when its context does not bind (not rely on the caller's earlier Preflight).
func TestSafetyApplyRefusesUnboundContext(t *testing.T) {
	for _, kind := range []string{"no-locks", "header", "no-validate", "denied"} {
		t.Run(kind, func(t *testing.T) {
			g, c, f, _ := fixture()
			p, pre := Preflight(context.Background(), g, c.PreflightContext, f)
			if pre.Code != "preflight_complete" {
				t.Fatal(pre)
			}
			switch kind {
			case "no-locks":
				c.HeldLocks = nil
			case "header":
				c.Header.OperationID = "other"
			case "no-validate":
				c.Validate = nil
			case "denied":
				c.Validate = func(context.Context) error { return fixtureErr }
			}
			r := Apply(context.Background(), p, c, f)
			if r.Code == "group_complete" || f.creates != 0 {
				t.Fatalf("apply proceeded on unbound context: %+v creates=%d", r.Code, f.creates)
			}
		})
	}
}

// Forged evidence whose source/target differ from the authorized logical path but matches an existing link on disk.
func TestSafetyInspectForgedTargetAgainstMatchingDisk(t *testing.T) {
	g, c, f, _ := fixture()
	r := apply(t, g, c, f)
	if r.Outcome != effects.Applied {
		t.Fatal(r)
	}
	e := r.Evidence.Clone()
	e.Links[0].Source = "/attacker/choice"
	e.Links[0].Target = "/attacker/choice"
	v := f.links["a"]
	v.Target = "/attacker/choice"
	f.links["a"] = v // disk "matches" the forged evidence
	x := Inspect(context.Background(), g, e, c.PreflightContext, f)
	if x.Code == "inspected_complete" {
		t.Fatalf("forged unauthorized target accepted: %+v", x.Code)
	}
}

func TestSafetyInspectSourceChangedAfterComplete(t *testing.T) {
	g, c, f, _ := fixture()
	r := apply(t, g, c, f)
	f.sources["b"] = false // required source vanished after completion
	x := Inspect(context.Background(), g, r.Evidence, c.PreflightContext, f)
	if x.Code == "inspected_complete" {
		t.Fatalf("complete reported although a required source vanished: %+v", x.Code)
	}
}

func TestSafetyInspectEvidenceShapeGuards(t *testing.T) {
	for _, kind := range []string{"root", "short", "long"} {
		t.Run(kind, func(t *testing.T) {
			g, c, f, _ := fixture()
			r := apply(t, g, c, f)
			e := r.Evidence.Clone()
			switch kind {
			case "root":
				e.RootID = "other-candidate"
			case "short":
				e.Links = e.Links[:1]
			case "long":
				e.Links = append(e.Links, e.Links[0])
			}
			x := Inspect(context.Background(), g, e, c.PreflightContext, f)
			if x.Code == "inspected_complete" || x.Code == "inspected_interrupted" {
				t.Fatalf("mismatched evidence accepted: %s", x.Code)
			}
		})
	}
}

func TestSafetyResolveHomeGuards(t *testing.T) {
	// logical path escapes the allowed base while the canonical observation stays inside
	i, o := homeInput()
	i.Path = "/elsewhere/provider"
	if _, err := ResolveRealHome(i, o); err == nil {
		t.Errorf("logical path outside AllowedBase accepted")
	}
	// planted root that CONTAINS the home but sits inside the allowed base
	i, o = homeInput()
	i.PlantedRoots = []string{"/resource"}
	o.CanonicalPlantedRoots = []string{"/resource"}
	i.AllowedBase = "/"
	o.CanonicalBase = "/"
	if _, err := ResolveRealHome(i, o); err == nil {
		t.Errorf("home beneath a planted root accepted")
	}
}
