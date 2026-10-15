package workspace

import (
	"testing"

	"github.com/hollis-labs/substrate/harness/sandbox"
	"github.com/hollis-labs/substrate/harness/workspace/credentials"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
)

func credentialPlanInputs(t *testing.T) (Spec, ResolvedContent, Resources, Observations) {
	s, c, r, o := fixturePlanInputs(t)
	grant := EffectGrant{Kind: CredentialLinkEffect, RootID: s.Boot.Candidate.ID, AuthorizationID: "link-grant", Version: "1"}
	s.Effects = append(s.Effects, grant)
	r.Grants = append(r.Grants, grant)
	r.Capabilities = append(r.Capabilities, CredentialLinks)
	o.Capabilities = append(o.Capabilities, CredentialLinks)
	s.Credentials = []CredentialSpec{{Source: ResourceRef{ID: "source", Path: "/fixture-source/captured/auth.json"}, DestinationRootID: s.Boot.Candidate.ID, Destination: "auth.json", Concern: "fixture", Required: true, Authorization: ResourceRef{ID: grant.AuthorizationID, Revision: grant.Version}, Access: []sandbox.AccessKind{sandbox.AccessSourceRead}}}
	s.EffectInputs.Credentials = []credentials.Group{{Layer: credentials.BootLayer, Candidate: effectRoot(s.Boot.Candidate, o.Roots[3], s.Boot.IdentityRoot.Path), Home: credentials.ResolvedHome{BeforeRedirect: true, LogicalPath: "/fixture-source/captured", CanonicalPath: "/fixture-source/captured", CanonicalBase: "/fixture-source", Provider: "claude", CaptureID: "capture", Revision: "1", Provenance: "fixture", PlantedRoots: []string{s.Home.Root.Path, s.Boot.IdentityRoot.Path}}, Bindings: []credentials.Binding{{Source: "auth.json", Destination: "auth.json", Required: true, AuthorizationID: grant.AuthorizationID, AuthorizationVersion: grant.Version, SourceRead: true}}}}
	return s, c, r, o
}

func TestPlanFreezesAndBindsExplicitCredentialInputs(t *testing.T) {
	s, c, r, o := credentialPlanInputs(t)
	p, err := Plan(s, c, r, o)
	if err != nil {
		t.Fatal(err)
	}
	g := p.EffectInputs().Credentials[0]
	if g.Header != (effects.Header{Version: effects.SchemaVersion, OperationID: s.OperationID, InputDigest: p.Digest()}) {
		t.Fatal("unbound effect header")
	}
	s.EffectInputs.Credentials[0].Home.CaptureID = "another-capture"
	q, err := Plan(s, c, r, o)
	if err != nil || q.Digest() == p.Digest() {
		t.Fatal("capture attestation did not enter digest", err)
	}
	s.EffectInputs.Credentials[0].Bindings[0].Destination = "other.json"
	g.Home.PlantedRoots[0] = "edited"
	if got := p.EffectInputs().Credentials[0]; got.Home.CaptureID != "capture" || got.Bindings[0].Destination != "auth.json" || got.Home.PlantedRoots[0] == "edited" {
		t.Fatal("caller changed frozen effects")
	}
}

func TestPlanRefusesUnboundCredentialInputs(t *testing.T) {
	for name, mutate := range map[string]func(*Spec, *Resources){
		"header":        func(s *Spec, _ *Resources) { s.EffectInputs.Credentials[0].Header.OperationID = "foreign" },
		"destination":   func(s *Spec, _ *Resources) { s.EffectInputs.Credentials[0].Bindings[0].Destination = "elsewhere" },
		"candidate":     func(s *Spec, _ *Resources) { s.EffectInputs.Credentials[0].Candidate.Path = s.Boot.Current.Path },
		"custody":       func(s *Spec, _ *Resources) { s.EffectInputs.Credentials[0].Candidate.PrivateCustody = false },
		"authorization": func(s *Spec, _ *Resources) { s.EffectInputs.Credentials[0].Bindings[0].AuthorizationID = "foreign" },
		"source write":  func(s *Spec, _ *Resources) { s.EffectInputs.Credentials[0].Bindings[0].SourceWrite = true },
	} {
		t.Run(name, func(t *testing.T) {
			s, c, r, o := credentialPlanInputs(t)
			mutate(&s, &r)
			if _, err := Plan(s, c, r, o); err == nil {
				t.Fatal("unbound effect accepted")
			}
		})
	}
}

func TestRecoveryRefusesSameOperationWithDifferentInputDigest(t *testing.T) {
	s, c, r, o := credentialPlanInputs(t)
	p, err := Plan(s, c, r, o)
	if err != nil {
		t.Fatal(err)
	}
	prior := Receipt{SchemaVersion: SchemaVersion, OperationID: p.spec.OperationID, InputDigest: "different", IdentityKey: p.spec.Identity.EncodedKey}
	result := ApplyResult{}
	if err := carryRecovery(&result, p, Observations{Receipts: []Receipt{prior}}); err == nil {
		t.Fatal("same operation accepted foreign input receipt")
	}
}
