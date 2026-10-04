package local

import (
	"slices"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace"
)

func TestResourceGrantTupleOrderWithEmbeddedNul(t *testing.T) {
	a := workspace.Resources{Grants: []workspace.EffectGrant{
		{Kind: workspace.TrustEffect, RootID: "x", AuthorizationID: "y\x00z", Version: "1"},
		{Kind: workspace.TrustEffect, RootID: "x\x00y", AuthorizationID: "z", Version: "1"},
	}}
	b := a
	b.Grants = slices.Clone(a.Grants)
	slices.Reverse(b.Grants)
	if !sameResources(a, b) {
		t.Fatal("grant set order changed resource authority equality")
	}
	b.Grants[0].Version = "2"
	if sameResources(a, b) {
		t.Fatal("different grant version matched resource authority")
	}
}
