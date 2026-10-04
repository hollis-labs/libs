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

func TestArtifactOnlyCanonicalActionAndDigest(t *testing.T) {
	s, c, r, o := fixturePlanInputs(t)
	r.Roots = []RootRef{s.Boot.Candidate}
	r.Grants = []EffectGrant{{Kind: ArtifactEffect, RootID: s.Boot.Candidate.ID, AuthorizationID: "fixture", Version: "1"}}
	input := TreeRequest{OperationID: "fixture-operation", Root: s.Boot.Candidate, RootMode: 0700, Tree: c.Rendered[0].Tree, Resources: r, Observed: o}
	p, err := planTree(input)
	if err != nil {
		t.Fatal(err)
	}
	if p.Digest() != "88376fa030b1b12a1118845bdc36c318e9b88e317c4c133c11b2bc51aa5ac6ea" {
		t.Fatalf("digest=%s", p.Digest())
	}
	for i := range input.Observed.Roots {
		ref := &input.Observed.Roots[i]
		ref.CanonicalBase = "/fixture-physical"
		ref.CanonicalPath = "/fixture-physical" + ref.DeclaredPath[len("/fixture"):]
	}
	aliased, err := planTree(input)
	if err != nil {
		t.Fatal(err)
	}
	action := aliased.Actions()[0]
	if action.CanonicalPath != "/fixture-physical"+input.Root.Path[len("/fixture"):] || action.Request.TargetRoot != action.CanonicalPath || action.Grant != r.Grants[0] || aliased.Digest() == p.Digest() {
		t.Fatal("canonical action or digest omitted physical authority")
	}
}
