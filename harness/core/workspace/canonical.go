package workspace

import (
	"cmp"
	"encoding/json"
	"slices"
)

// DigestVersion pins the domain and encoding of receipt input digests. Any
// semantic encoding change requires a new version and a reviewed golden.
const DigestVersion = "workspace.plan.input.v1"

func canonicalInputs(s Spec, r Resources) (Spec, Resources) {
	s = copyRecord(s)
	r = copyRecord(r)
	effectLess := func(a, b EffectGrant) int {
		return cmp.Compare(string(a.Kind)+"\x00"+a.RootID+"\x00"+a.AuthorizationID+"\x00"+a.Version, string(b.Kind)+"\x00"+b.RootID+"\x00"+b.AuthorizationID+"\x00"+b.Version)
	}
	slices.SortFunc(s.Effects, effectLess)
	slices.SortFunc(r.Grants, effectLess)
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
