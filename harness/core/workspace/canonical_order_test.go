package workspace

import (
	"slices"
	"testing"
)

func TestRequestedAndAvailableGrantTupleOrdersAgree(t *testing.T) {
	s, c, r, o := fixturePlanInputs(t)
	for i, id := range []string{"x", "x\x00a"} {
		ref := fixtureRoot([]string{"effect-one", "effect-two"}[i])
		ref.ID = id
		r.Roots = append(r.Roots, ref)
		o.Roots = append(o.Roots, RootObservation{RootID: id, DeclaredPath: ref.Path, CanonicalPath: ref.Path, CanonicalBase: ref.AllowedBase, Owner: ref.Owner})
		s.Effects = append(s.Effects, EffectGrant{Kind: TrustEffect, RootID: id, AuthorizationID: []string{"b", "z"}[i], Version: "1"})
	}
	r.Grants = slices.Clone(s.Effects)
	p := fixturePlanned(t, s, c, r, o)
	if !slices.Equal(p.resources.Grants, p.spec.Effects) {
		t.Fatal("requested and available grant tuple orders diverged")
	}
}
