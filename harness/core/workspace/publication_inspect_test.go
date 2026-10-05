package workspace_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/publication"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func inspectionFixture(t *testing.T) (workspace.Scope, publication.Journal, []workspace.Receipt) {
	t.Helper()
	s, _, r, _ := planInputs(t)
	aside := s.Boot.Candidate
	aside.ID = "aside"
	aside.Path = filepath.Join(s.Boot.IdentityRoot.Path, "prior-original-operation")
	r.Roots = append(r.Roots, aside)
	scope, err := workspace.ResolveScope(s, r, r.LockRoot)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(ref workspace.RootRef) publication.Root {
		return publication.Root{ID: ref.ID, Path: ref.Path, Owner: ref.Owner, Provenance: ref.Provenance}
	}
	parent, current, candidate, old := mk(s.Boot.IdentityRoot), mk(s.Boot.Current), mk(s.Boot.Candidate), mk(aside)
	parent.Identity = publication.FileIdentity{Volume: "fixture", Device: 1, Inode: 1}
	candidate.Identity = publication.FileIdentity{Volume: "fixture", Device: 1, Inode: 2}
	j := publication.Journal{Version: publication.Version, JournalID: "original-journal", Control: publication.Root{ID: scope.Control().ID, Path: scope.Control().Path, Owner: scope.Control().Owner, Provenance: scope.Control().Provenance, Identity: publication.FileIdentity{Volume: "control", Device: 2, Inode: 5}}, Origin: publication.Origin{OperationID: "original", InputDigest: strings.Repeat("a", 64), AgentURN: s.Identity.AgentURN, IdentityKey: s.Identity.EncodedKey}, Layout: publication.Layout{Parent: parent, Current: current, Candidate: candidate, Aside: old, CandidateGeneration: "generation", CandidateManifestDigest: strings.Repeat("b", 64)}, Use: publication.UseBinding{Namespace: r.LockNamespace, CanonicalID: parent.Path, MutationPath: filepath.Join(r.LockNamespace, "existing-lock"), PinPath: filepath.Join(r.LockNamespace, "recorded-pin"), MutationIdentity: publication.FileIdentity{Volume: "control", Device: 2, Inode: 3}, PinIdentity: publication.FileIdentity{Volume: "control", Device: 2, Inode: 4}, ReservationID: "fixture-reservation"}, Events: []publication.Event{{Sequence: 1, Phase: publication.Planned}}}
	receipt := workspace.Receipt{SchemaVersion: workspace.SchemaVersion, OperationID: j.Origin.OperationID, InputDigest: j.Origin.InputDigest, IdentityKey: j.Origin.IdentityKey, Phase: workspace.Interrupted, Obligations: []workspace.Obligation{{Kind: workspace.EffectPending, RootID: "repository", Code: "original_uncertain"}}}
	retry := receipt
	retry.OperationID = "retry"
	retry.InputDigest = strings.Repeat("c", 64)
	return scope, j, []workspace.Receipt{receipt, retry}
}

func TestPublicationInspectionKeepsFourOperationOriginsAndUncertainObligations(t *testing.T) {
	scope, j, receipts := inspectionFixture(t)
	for i, operation := range []string{"third", "fourth"} {
		r := receipts[0]
		r.OperationID = operation
		r.InputDigest = strings.Repeat([]string{"d", "e"}[i], 64)
		r.Phase = workspace.ArtifactsCommitted
		r.Obligations = []workspace.Obligation{{Kind: workspace.EffectPending, RootID: operation, Code: "earlier_uncertain"}}
		receipts = append(receipts, r)
	}
	raw, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	out, err := workspace.InspectPublicationEvidence(scope, workspace.Recover, raw, receipts)
	if err != nil || out.Status != workspace.Partial || !reflect.DeepEqual(out.OriginalReceipts, receipts) {
		t.Fatalf("original operations replaced by current retry: %+v %v", out, err)
	}
	for _, receipt := range receipts {
		for _, original := range receipt.Obligations {
			found := false
			for _, retained := range out.Obligations {
				found = found || retained == original
			}
			if !found {
				t.Fatalf("successful later artifacts erased obligation: %+v", original)
			}
		}
	}
	for _, kind := range []string{"operation-rebind", "identity", "terminal-phase", "noncanonical-digest", "missing-origin"} {
		t.Run(kind, func(t *testing.T) {
			bad := append([]workspace.Receipt(nil), receipts...)
			switch kind {
			case "operation-rebind":
				bad[3].OperationID = bad[0].OperationID
			case "identity":
				bad[3].IdentityKey = "foreign"
			case "terminal-phase":
				bad[3].Phase = "future"
			case "noncanonical-digest":
				bad[3].InputDigest = strings.ToUpper(bad[3].InputDigest)
			case "missing-origin":
				bad = bad[1:]
			}
			refused, err := workspace.InspectPublicationEvidence(scope, workspace.Recover, raw, bad)
			if err == nil || refused.Status != workspace.Partial || len(refused.Retained) == 0 {
				t.Fatalf("foreign origin erased roots or admitted: %+v %v", refused, err)
			}
		})
	}
}
func TestPublicationInspectionRetainsAllOriginalObligationsAndRefusesRetirement(t *testing.T) {
	scope, j, receipts := inspectionFixture(t)
	raw, _ := json.Marshal(j)
	out, err := workspace.InspectPublicationEvidence(scope, workspace.Recover, raw, receipts)
	if err != nil || out.Status != workspace.Partial || len(out.OriginalReceipts) != 2 || len(out.Obligations) < 2 {
		t.Fatalf("inspection: %+v %v", out, err)
	}
	out.OriginalReceipts[0].Obligations[0].Code = "changed"
	if receipts[0].Obligations[0].Code == "changed" {
		t.Fatal("receipt evidence alias")
	}
	retired, err := workspace.InspectPublicationEvidence(scope, workspace.Retire, raw, receipts)
	if err == nil || retired.Status != workspace.Unsupported || len(retired.Obligations) < 2 || len(retired.Retained) == 0 {
		t.Fatal("retirement invented complete absence")
	}
}
func TestPublicationInspectionRejectsForeignOriginsAndUnknownJournal(t *testing.T) {
	for _, kind := range []string{"origin-digest", "origin-operation", "aside", "owner", "namespace", "torn"} {
		t.Run(kind, func(t *testing.T) {
			scope, j, receipts := inspectionFixture(t)
			switch kind {
			case "origin-digest":
				j.Origin.InputDigest = strings.Repeat("f", 64)
			case "origin-operation":
				j.Origin.OperationID = "foreign"
			case "aside":
				j.Layout.Aside.ID = "foreign"
			case "owner":
				j.Layout.Parent.Owner = "foreign"
				j.Layout.Current.Owner = "foreign"
				j.Layout.Candidate.Owner = "foreign"
				j.Layout.Aside.Owner = "foreign"
			case "namespace":
				j.Use.Namespace = filepath.Join(scope.Control().AllowedBase, "foreign")
				j.Use.MutationPath = filepath.Join(j.Use.Namespace, "lock")
				j.Use.PinPath = filepath.Join(j.Use.Namespace, "pin")
			}
			raw, _ := json.Marshal(j)
			if kind == "torn" {
				raw = raw[:len(raw)-1]
			}
			out, err := workspace.InspectPublicationEvidence(scope, workspace.Recover, raw, receipts)
			if err == nil || len(out.Retained) == 0 {
				t.Fatal("foreign or unknown journal accepted or roots forgotten")
			}
		})
	}
}

func TestPublicationInspectionAdmitsEveryNestedOrigin(t *testing.T) {
	for _, variant := range []string{"own-journal", "inherited-journal", "invalid-pin"} {
		t.Run(variant, func(t *testing.T) {
			scope, j, receipts := inspectionFixture(t)
			foreign := j.Clone()
			foreign.Origin.OperationID = "foreign-original-operation"
			switch variant {
			case "own-journal":
				receipts[0].PublicationJournal = &foreign
			case "inherited-journal":
				receipts[0].PublicationOrigins = []workspace.PublicationOrigin{{SchemaVersion: workspace.SchemaVersion, OperationID: receipts[0].OperationID, InputDigest: receipts[0].InputDigest, IdentityKey: receipts[0].IdentityKey, Journal: foreign}}
			case "invalid-pin":
				receipts[0].PinCreation = &workspace.PinCreationEvidence{Version: "foreign-pin-version"}
			}
			raw, err := json.Marshal(j)
			if err != nil {
				t.Fatal(err)
			}
			out, err := workspace.InspectPublicationEvidence(scope, workspace.Recover, raw, receipts)
			if err == nil {
				t.Fatalf("original receipt with foreign embedded journal admitted: status=%s originals=%d", out.Status, len(out.OriginalReceipts))
			}
		})
	}
}

func inspectionPinEvidence(scope workspace.Scope, j publication.Journal) workspace.PinCreationEvidence {
	key := workspace.LockKey{Namespace: scope.Resources().LockNamespace, CanonicalID: scope.Spec().Boot.IdentityRoot.Path}
	sum := sha256.Sum256([]byte(key.CanonicalID))
	return workspace.PinCreationEvidence{Version: workspace.PinCreationVersion, Origin: j.Origin, Control: scope.Control(), Key: key, Path: filepath.Join(key.Namespace, "pin-"+hex.EncodeToString(sum[:])), JournalID: j.JournalID, ReservationID: j.Use.ReservationID, Grant: workspace.EffectGrant{Kind: workspace.PinCreationEffect, RootID: scope.Spec().Boot.IdentityRoot.ID, AuthorizationID: "original-pin-grant", Version: "1"}, Created: true, Uncertain: true, Identity: workspace.NativePinIdentity{Device: 2, Inode: 4}}
}

func TestPublicationInspectionPreservesOlderNestedOriginsAndEnrolledRoots(t *testing.T) {
	scope, j, receipts := inspectionFixture(t)
	pin := inspectionPinEvidence(scope, j)
	if e := pin.Validate(); e != nil {
		t.Fatal(e)
	}
	receipts[0].PublicationJournal = &j
	receipts[0].PinCreation = &pin
	// The inherited operation and enrolled candidate/Aside are different from
	// both the selected journal and the newer aggregate; never normalize them.
	prior := j.Clone()
	prior.Origin.OperationID = "earlier-nested"
	prior.Origin.InputDigest = strings.Repeat("d", 64)
	spec, resources := scope.Spec(), scope.Resources()
	candidate, aside := spec.Boot.Candidate, spec.Boot.Candidate
	candidate.ID = "earlier-candidate"
	candidate.Path = filepath.Join(spec.Boot.IdentityRoot.Path, "earlier-candidate")
	aside.ID = "earlier-aside"
	aside.Path = filepath.Join(spec.Boot.IdentityRoot.Path, "earlier-aside")
	resources.Roots = append(resources.Roots, candidate, aside)
	var err error
	scope, err = workspace.ResolveScope(spec, resources, scope.Control())
	if err != nil {
		t.Fatal(err)
	}
	prior.Layout.Candidate.ID = candidate.ID
	prior.Layout.Candidate.Path = candidate.Path
	prior.Layout.Aside.ID = aside.ID
	prior.Layout.Aside.Path = aside.Path
	receipts[1].PublicationOrigins = []workspace.PublicationOrigin{{SchemaVersion: workspace.SchemaVersion, OperationID: prior.Origin.OperationID, InputDigest: prior.Origin.InputDigest, IdentityKey: prior.Origin.IdentityKey, Journal: prior}}
	olderPin := pin
	olderPin.Origin = prior.Origin
	receipts[1].PinOrigins = []workspace.PinCreationOrigin{{SchemaVersion: workspace.SchemaVersion, OperationID: olderPin.Origin.OperationID, InputDigest: olderPin.Origin.InputDigest, IdentityKey: olderPin.Origin.IdentityKey, Evidence: olderPin}}
	raw, _ := json.Marshal(j)
	out, err := workspace.InspectPublicationEvidence(scope, workspace.Recover, raw, receipts)
	if err != nil || !reflect.DeepEqual(out.OriginalReceipts, receipts) {
		t.Fatalf("valid older origins rebound or erased: %+v %v", out, err)
	}
	out.OriginalReceipts[1].PublicationOrigins[0].Journal.Layout.Candidate.Owner = "mutated"
	if receipts[1].PublicationOrigins[0].Journal.Layout.Candidate.Owner == "mutated" {
		t.Fatal("returned older envelope aliases input")
	}
	for _, kind := range []string{"own-pin-operation", "own-pin-digest", "inherited-pin-operation", "inherited-pin-digest", "pin-schema", "pin-key", "pin-scope", "pin-root", "publication-schema", "publication-key", "publication-scope", "candidate-unenrolled", "candidate-owner", "aside-unenrolled", "cross-family-digest", "nested-outer-digest"} {
		t.Run(kind, func(t *testing.T) {
			data, _ := json.Marshal(receipts)
			var bad []workspace.Receipt
			if err := json.Unmarshal(data, &bad); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "own-pin-operation":
				bad[0].PinCreation.Origin.OperationID = "foreign"
			case "own-pin-digest":
				bad[0].PinCreation.Origin.InputDigest = strings.Repeat("e", 64)
			case "inherited-pin-operation":
				bad[1].PinOrigins[0].Evidence.Origin.OperationID = "foreign"
			case "inherited-pin-digest":
				bad[1].PinOrigins[0].Evidence.Origin.InputDigest = strings.Repeat("e", 64)
			case "pin-schema":
				bad[1].PinOrigins[0].SchemaVersion = "foreign"
			case "pin-key":
				bad[1].PinOrigins[0].IdentityKey = "foreign"
			case "pin-root":
				bad[1].PinOrigins[0].Evidence.Grant.RootID = "unenrolled"
			case "pin-scope":
				e := &bad[1].PinOrigins[0].Evidence
				e.Control.Owner = "foreign"
			case "publication-schema":
				bad[1].PublicationOrigins[0].SchemaVersion = "foreign"
			case "publication-key":
				bad[1].PublicationOrigins[0].IdentityKey = "foreign"
			case "publication-scope":
				bad[1].PublicationOrigins[0].Journal.Control.Owner = "foreign"
			case "candidate-unenrolled":
				bad[1].PublicationOrigins[0].Journal.Layout.Candidate.ID = "unenrolled"
			case "candidate-owner":
				bad[1].PublicationOrigins[0].Journal.Layout.Candidate.Owner = "foreign"
			case "aside-unenrolled":
				bad[1].PublicationOrigins[0].Journal.Layout.Aside.ID = "unenrolled"
			case "cross-family-digest":
				bad[1].PinOrigins[0].InputDigest = strings.Repeat("e", 64)
				bad[1].PinOrigins[0].Evidence.Origin.InputDigest = strings.Repeat("e", 64)
			case "nested-outer-digest":
				bad[1].PublicationOrigins[0].OperationID = bad[0].OperationID
				bad[1].PublicationOrigins[0].Journal.Origin.OperationID = bad[0].OperationID
			}
			refused, err := workspace.InspectPublicationEvidence(scope, workspace.Recover, raw, bad)
			if err == nil || len(refused.Retained) == 0 || len(refused.OriginalReceipts) == len(bad) {
				t.Fatal("foreign nested origin returned or roots forgotten")
			}
		})
	}
}
