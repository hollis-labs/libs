package workspace

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func publicationInputs(t *testing.T) (Spec, ResolvedContent, Resources, Observations) {
	t.Helper()
	s, c, r, o := fixturePlanInputs(t)
	aside := s.Boot.Candidate
	aside.ID, aside.Path = "aside", filepath.Join(s.Boot.IdentityRoot.Path, "aside-original")
	g := EffectGrant{Kind: PublicationEffect, RootID: s.Boot.IdentityRoot.ID, AuthorizationID: "publish-authority", Version: "1"}
	pin := EffectGrant{Kind: PinCreationEffect, RootID: s.Boot.IdentityRoot.ID, AuthorizationID: "pin-authority", Version: "1"}
	s.Publication = &PublicationSpec{Control: r.LockRoot, Aside: aside, JournalID: "journal-original", ReservationID: "reservation-original", Authorization: g, PinCreationAuthorization: pin}
	s.Effects = append(s.Effects, g, pin)
	r.Grants = slices.Clone(s.Effects)
	r.Roots = append(r.Roots, aside)
	fixtureObservation(&o, s.Boot.IdentityRoot.ID).Exists = true
	fixtureObservation(&o, s.Boot.IdentityRoot.ID).Directory = true
	o.Roots = append(o.Roots, RootObservation{RootID: aside.ID, DeclaredPath: aside.Path, CanonicalPath: aside.Path, CanonicalBase: aside.AllowedBase, Owner: aside.Owner})
	return s, c, r, o
}

func TestPublicationRequestRejectsMissingAndForeignAuthority(t *testing.T) {
	for _, change := range []struct {
		name string
		edit func(*Spec, *Resources, *Observations)
	}{
		{"empty", func(s *Spec, _ *Resources, _ *Observations) { s.Publication = &PublicationSpec{} }},
		{"foreign-control", func(s *Spec, _ *Resources, _ *Observations) { s.Publication.Control.Owner = "foreign" }},
		{"current-aside", func(s *Spec, _ *Resources, _ *Observations) { s.Publication.Aside = s.Boot.Current }},
		{"grant-rebound", func(s *Spec, _ *Resources, _ *Observations) { s.Publication.Authorization.AuthorizationID = "foreign" }},
		{"pin-grant-rebound", func(s *Spec, _ *Resources, _ *Observations) {
			s.Publication.PinCreationAuthorization.Version = "foreign"
		}},
		{"missing-pin-intent", func(s *Spec, _ *Resources, _ *Observations) { s.Effects = s.Effects[:len(s.Effects)-1] }},
		{"unenrolled-aside", func(_ *Spec, r *Resources, _ *Observations) { r.Roots = r.Roots[:len(r.Roots)-1] }},
		{"occupied-aside", func(_ *Spec, _ *Resources, o *Observations) {
			o.Roots[len(o.Roots)-1].Exists = true
			o.Roots[len(o.Roots)-1].Directory = true
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			s, c, r, o := publicationInputs(t)
			change.edit(&s, &r, &o)
			if p, err := Plan(s, c, r, o); err == nil || p.Valid() {
				t.Fatal("publication request silently ignored malformed/foreign authority")
			}
		})
	}
}

func TestPublicationUnknownMetadataRefusesBeforeAnyApplyPort(t *testing.T) {
	s, c, r, o := publicationInputs(t)
	p := fixturePlanned(t, s, c, r, o)
	result, err := Materialize(context.Background(), p, Ports{})
	if err == nil || result.Status != Unsupported || result.ArtifactsComplete() || result.LaunchReady() {
		t.Fatalf("unknown publication capability reached apply: %+v %v", result, err)
	}
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != "publication_metadata_unavailable" {
		t.Fatalf("publication refusal not explicit: %v", err)
	}
}

func TestPublicationFreezeDigestAndExistingCompleteMutationUnion(t *testing.T) {
	s, c, r, o := publicationInputs(t)
	p := fixturePlanned(t, s, c, r, o)
	keys := p.LockKeys()
	parent := LockKey{Namespace: r.LockNamespace, CanonicalID: s.Boot.IdentityRoot.Path}
	if !slices.Contains(keys, parent) || len(keys) != 2 {
		t.Fatalf("publication changed the existing home/parent mutation union: %+v", keys)
	}
	detached := p.Publication()
	detached.ReservationID = "foreign"
	if p.Publication().ReservationID != s.Publication.ReservationID {
		t.Fatal("publication accessor changes immutable request")
	}
	for _, kind := range []string{"journal", "reservation", "aside", "control", "publish-grant", "pin-grant"} {
		t.Run(kind, func(t *testing.T) {
			s2, r2, o2 := copyRecord(s), copyRecord(r), copyRecord(o)
			switch kind {
			case "journal":
				s2.Publication.JournalID += "-other"
			case "reservation":
				s2.Publication.ReservationID += "-other"
			case "aside":
				s2.Publication.Aside.Path += "-other"
				r2.Roots[len(r2.Roots)-1] = s2.Publication.Aside
				fixtureObservation(&o2, s2.Publication.Aside.ID).DeclaredPath = s2.Publication.Aside.Path
				fixtureObservation(&o2, s2.Publication.Aside.ID).CanonicalPath = s2.Publication.Aside.Path
			case "control":
				s2.Publication.Control.Provenance += "-other"
				r2.LockRoot = s2.Publication.Control
			case "publish-grant", "pin-grant":
				grant := &s2.Publication.Authorization
				if kind == "pin-grant" {
					grant = &s2.Publication.PinCreationAuthorization
				}
				old := *grant
				grant.Version += "-other"
				for i, value := range s2.Effects {
					if value == old {
						s2.Effects[i] = *grant
					}
				}
				for i, value := range r2.Grants {
					if value == old {
						r2.Grants[i] = *grant
					}
				}
			}
			other := fixturePlanned(t, s2, c, r2, o2)
			if other.Digest() == p.Digest() {
				t.Fatal("semantic publication authority/binding omitted from digest")
			}
			if !reflect.DeepEqual(other.LockKeys(), keys) {
				t.Fatal("generation/journal change invented another mutation domain")
			}
		})
	}
	artifactSpec := copyRecord(s)
	artifactSpec.Publication = nil
	artifactSpec.Effects = artifactSpec.Effects[:len(artifactSpec.Effects)-2]
	artifactResources := copyRecord(r)
	artifactResources.Grants = slices.Clone(artifactSpec.Effects)
	artifactPlan := fixturePlanned(t, artifactSpec, c, artifactResources, o)
	if artifactPlan.Publication() != nil || artifactPlan.Digest() == p.Digest() {
		t.Fatal("publication silently collapsed to artifact-only input")
	}
}

func TestPublicationUnsupportedRetainsOriginalRecoveryWithoutApplyCallbacks(t *testing.T) {
	s, c, r, o := publicationInputs(t)
	obligation := Obligation{Kind: RecoveryInspectionRequired, RootID: s.Boot.Candidate.ID, Code: "earlier-uncertain-effect"}
	original := Receipt{SchemaVersion: SchemaVersion, OperationID: "earlier-operation", InputDigest: s.Identity.ArtifactDigest.Hex, IdentityKey: s.Identity.EncodedKey, Phase: Interrupted, Roots: []RootReceipt{{Root: s.Boot.Candidate}}, Obligations: []Obligation{obligation}}
	r.RecoveryReceipts = []Receipt{original}
	p := fixturePlanned(t, s, c, r, o)
	result, err := Materialize(context.Background(), p, Ports{})
	if err == nil || result.Status != Partial || result.ArtifactsComplete() || result.LaunchReady() || !slices.Contains(result.Retained, s.Boot.Candidate) || !slices.Contains(result.Obligations, obligation) {
		t.Fatalf("unavailable admission erased trusted earlier obligation: %+v %v", result, err)
	}
	r.RecoveryReceipts[0].IdentityKey = "foreign"
	bad := fixturePlanned(t, s, c, r, o)
	result, err = Materialize(context.Background(), bad, Ports{})
	if err == nil || result.Status != Conflict || result.ArtifactsComplete() {
		t.Fatal("foreign original admitted by unavailable publication path")
	}
}

func TestPinOriginsPreserveOriginalOperationAcrossRepeatedUnavailableRetries(t *testing.T) {
	s, c, r, o := publicationInputs(t)
	first := fixturePlanned(t, s, c, r, o)
	intent, err := first.PinCreationIntent()
	if err != nil {
		t.Fatal(err)
	}
	obligation := Obligation{Kind: RecoveryInspectionRequired, RootID: s.Boot.IdentityRoot.ID, Code: "original-created-or-uncertain-pin"}
	intent.Uncertain = true // Original durable intent/uncertain transport, no created inode asserted.
	receipt := Receipt{SchemaVersion: SchemaVersion, OperationID: s.OperationID, InputDigest: first.Digest(), IdentityKey: s.Identity.EncodedKey, Identity: s.Identity, Phase: Interrupted, PinCreation: &intent, Obligations: []Obligation{obligation}}
	for _, operation := range []string{"retry-one", "retry-two", "retry-three"} {
		s.OperationID = operation
		r.RecoveryReceipts = []Receipt{receipt}
		p := fixturePlanned(t, s, c, r, o)
		result, err := Materialize(context.Background(), p, Ports{})
		if err == nil || result.Status != Partial || result.ArtifactsComplete() || result.LaunchReady() || !slices.Contains(result.Obligations, obligation) || len(result.Receipt.PinOrigins) != 1 || result.Receipt.PinOrigins[0].OperationID != first.OperationID() || result.Receipt.PinOrigins[0].InputDigest != first.Digest() || result.Receipt.PinOrigins[0].Evidence != intent {
			t.Fatalf("retry rebound original pin intent: %+v %v", result, err)
		}
		receipt = result.Receipt
	}
}

func TestPinOriginCoherentInnerSpliceRefusesBeforeAnyApplyPort(t *testing.T) {
	for _, kind := range []string{"own-operation", "own-digest", "inherited-operation", "inherited-digest", "foreign-control", "foreign-key", "foreign-grant-root"} {
		t.Run(kind, func(t *testing.T) {
			s, c, r, o := publicationInputs(t)
			original := fixturePlanned(t, s, c, r, o)
			evidence, err := original.PinCreationIntent()
			if err != nil {
				t.Fatal(err)
			}
			e := &evidence
			receipt := Receipt{SchemaVersion: SchemaVersion, OperationID: s.OperationID, InputDigest: original.Digest(), IdentityKey: s.Identity.EncodedKey, Identity: s.Identity, Phase: Interrupted, PinCreation: e}
			if kind == "inherited-operation" || kind == "inherited-digest" {
				s.OperationID = "intermediate"
				r.RecoveryReceipts = []Receipt{receipt}
				middle := fixturePlanned(t, s, c, r, o)
				result, _ := Materialize(context.Background(), middle, Ports{})
				receipt = result.Receipt
				e = &receipt.PinOrigins[0].Evidence
			}
			switch kind {
			case "own-operation", "inherited-operation":
				e.Origin.OperationID = "foreign"
			case "own-digest", "inherited-digest":
				e.Origin.InputDigest = s.Identity.SemanticDigest.Hex
			case "foreign-control":
				e.Control.Owner = "foreign"
			case "foreign-key":
				e.Key.CanonicalID += "-foreign"
			case "foreign-grant-root":
				e.Grant.RootID = s.Home.Root.ID
			}
			s.OperationID = "final-retry"
			r.RecoveryReceipts = []Receipt{receipt}
			p := fixturePlanned(t, s, c, r, o)
			result, err := Materialize(context.Background(), p, Ports{})
			if err == nil || result.Status != Conflict || result.ArtifactsComplete() || result.LaunchReady() {
				t.Fatalf("foreign original admitted before mutation: %+v %v", result, err)
			}
		})
	}
}
