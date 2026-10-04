package workspace

import (
	"cmp"
	"encoding/json"
	"slices"
)

// DigestVersion pins the domain and encoding of receipt input digests. Any
// semantic encoding change requires a new version and a reviewed golden.
const DigestVersion = "workspace.plan.input.v1"

func compareEffectGrants(a, b EffectGrant) int {
	if n := cmp.Compare(a.Kind, b.Kind); n != 0 {
		return n
	}
	if n := cmp.Compare(a.RootID, b.RootID); n != 0 {
		return n
	}
	if n := cmp.Compare(a.AuthorizationID, b.AuthorizationID); n != 0 {
		return n
	}
	return cmp.Compare(a.Version, b.Version)
}

func canonicalInputs(s Spec, r Resources) (Spec, Resources) {
	s = copyRecord(s)
	r = copyRecord(r)
	slices.SortFunc(s.Effects, compareEffectGrants)
	slices.SortFunc(r.Grants, compareEffectGrants)
	r.Grants = slices.CompactFunc(r.Grants, func(a, b EffectGrant) bool { return a == b })
	slices.Sort(r.Capabilities)
	r.Capabilities = slices.Compact(r.Capabilities)
	slices.SortFunc(r.ProviderHomes, func(a, b ResourceRef) int {
		aa, _ := json.Marshal(a)
		bb, _ := json.Marshal(b)
		return cmp.Compare(string(aa), string(bb))
	})
	r.ProviderHomes = slices.CompactFunc(r.ProviderHomes, func(a, b ResourceRef) bool { return a == b })
	slices.Sort(s.Sandbox.RequiredCapabilities)
	s.Sandbox.RequiredCapabilities = slices.Compact(s.Sandbox.RequiredCapabilities)
	slices.Sort(s.Cleanup.RequiredProofs)
	s.Cleanup.RequiredProofs = slices.Compact(s.Cleanup.RequiredProofs)
	return s, r
}
