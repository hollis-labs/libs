package workspace_test

import (
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
