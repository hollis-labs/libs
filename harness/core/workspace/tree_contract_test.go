package workspace

import (
	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"testing"
)

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
	if p.Digest() != "5a3d47ed958454e34c409051c070ef5ea74a5363c5905dc5476b485f6dc97e16" {
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

func TestArtifactOnlyEmptyFileAndProtocolInputs(t *testing.T) {
	s, _, r, o := fixturePlanInputs(t)
	r.Roots = []RootRef{s.Boot.Candidate}
	r.Grants = []EffectGrant{{Kind: ArtifactEffect, RootID: s.Boot.Candidate.ID, AuthorizationID: "fixture", Version: "1"}}
	input := TreeRequest{OperationID: "fixture-operation", Root: s.Boot.Candidate, RootMode: 0700, Resources: r, Observed: o, Tree: artifact.Tree{Entries: []artifact.Entry{{Path: "empty.txt", Kind: artifact.EntryFile, Mode: 0644, Bytes: []byte{}, Ownership: artifact.Ownership{EntryID: "empty", GroupID: "fixture"}, Provenance: artifact.Provenance{Source: "fixture"}}}}}
	plan, err := planTree(input)
	if err != nil {
		t.Fatal("authored empty file lost:", err)
	}
	if plan.actions[0].Request.Artifacts.Entries[0].Bytes == nil {
		t.Fatal("empty bytes became unresolved")
	}
	for name, mutate := range map[string]func(*TreeRequest){
		"generation":     func(r *TreeRequest) { r.Generation = "explicit-generation" },
		"operation":      func(r *TreeRequest) { r.Operation = materialize.OperationCreate },
		"metadata roots": func(r *TreeRequest) { r.TargetRoots.ProjectRoot = "/fixture/project" },
	} {
		t.Run(name, func(t *testing.T) {
			next := input
			mutate(&next)
			p, err := planTree(next)
			if err != nil {
				t.Fatal(err)
			}
			if p.Digest() == plan.Digest() {
				t.Fatal("protocol input omitted from digest")
			}
		})
	}
}
