package workspace

import "testing"

func TestArtifactOnlyPlanBindsInputBeforeApply(t *testing.T) {
	s, c, r, o := fixturePlanInputs(t)
	r.Roots = []RootRef{s.Boot.Candidate}
	r.Grants = []EffectGrant{{Kind: ArtifactEffect, RootID: s.Boot.Candidate.ID, AuthorizationID: "fixture", Version: "1"}}
	input := TreeRequest{OperationID: "fixture-operation", Root: s.Boot.Candidate, RootMode: 0700, Tree: c.Rendered[0].Tree, Resources: r, Observed: o}
	if _, err := planTree(input); err != nil {
		t.Fatal("valid fixture", err)
	}
	for name, mutate := range map[string]func(*TreeRequest){
		"utf8":             func(r *TreeRequest) { r.OperationID = string([]byte{0xff}) },
		"lock owner":       func(r *TreeRequest) { r.Resources.LockRoot.Owner = "other" },
		"lock declaration": func(r *TreeRequest) { r.Resources.LockRoot.Path += "-other" },
	} {
		t.Run(name, func(t *testing.T) {
			r := copyRecord(input)
			mutate(&r)
			if _, err := planTree(r); err == nil {
				t.Fatal("invalid artifact-only plan accepted")
			}
		})
	}
}
