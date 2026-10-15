package workspace

import (
	"context"
	"errors"
	"github.com/hollis-labs/substrate/harness/workspace/materialize"
	"github.com/hollis-labs/substrate/harness/workspace/snapshot"
	"testing"
)

func snapshotForkFixture(t *testing.T) TreeRequest {
	s, _, r, o := fixturePlanInputs(t)
	r.Roots = []RootRef{s.Boot.Candidate}
	r.Grants = []EffectGrant{{Kind: ArtifactEffect, RootID: s.Boot.Candidate.ID, AuthorizationID: "fixture", Version: "1"}}
	return TreeRequest{OperationID: "fixture-operation", Operation: materialize.OperationCreate, Root: s.Boot.Candidate, RootMode: 0700, Resources: r, Observed: o}
}
func TestSnapshotForkRefusesDecodedReceiptBeforePorts(t *testing.T) {
	request := SnapshotForkRequest{Source: &snapshot.RetainedSet{}, TargetID: "root", Input: snapshotForkFixture(t)}
	// Every port is nil. A caller-built/decoded source must refuse before any
	// destination creation or effect callback, even with valid plan-only inputs.
	result, e := ForkSnapshot(context.Background(), request, Ports{})
	if e == nil || result.Receipt != nil || len(result.Apply.Handles) != 0 || result.SourcePinRetained {
		t.Fatal("decoded source reached effects", e)
	}
	var refusal *Refusal
	if !errors.As(e, &refusal) || refusal.Code != "snapshot_fork_pin_unavailable" {
		t.Fatal("wrong refusal boundary", e)
	}
}
func TestSnapshotForkPreservesEmptyBytesAndPinnedProvenance(t *testing.T) {
	input := snapshotForkFixture(t)
	files := []snapshot.CapturedFile{{Path: "z/code", Mode: 0700, Bytes: []byte("captured")}, {Path: "empty", Mode: 0600, Bytes: []byte{}}}
	input.Tree = snapshotForkTree(files, "recorded-tree")
	files[0].Bytes[0] = 'X'
	files[0].Path = "caller-mutated"
	planned, e := planTree(input)
	if e != nil {
		t.Fatal(e)
	}
	entries := planned.actions[0].Request.Artifacts.Entries
	if entries[0].Path != "empty" || entries[0].Bytes == nil || entries[1].Path != "z/code" || string(entries[1].Bytes) != "captured" || entries[1].Mode != 0700 || entries[1].Provenance.Revision != "recorded-tree" {
		t.Fatal("fork-to-sole-engine projection lost immutable content")
	}
}
func TestSnapshotForkRequiresAbsentNewRoot(t *testing.T) {
	input := snapshotForkFixture(t)
	for i := range input.Observed.Roots {
		if input.Observed.Roots[i].RootID == input.Root.ID {
			input.Observed.Roots[i].Exists = true
			input.Observed.Roots[i].Directory = true
		}
	}
	result, e := ForkSnapshot(context.Background(), SnapshotForkRequest{Source: &snapshot.RetainedSet{}, TargetID: "root", Input: input}, Ports{})
	if result.Receipt != nil || e == nil {
		t.Fatal("existing root adopted")
	}
	var refusal *Refusal
	if !errors.As(e, &refusal) || refusal.Code != "snapshot_fork_destination_exists" {
		t.Fatal("wrong existing-root boundary", e)
	}
}
func TestSnapshotForkPathOverlapChecksBothDirections(t *testing.T) {
	for _, p := range [][2]string{{"/fixture/root", "/fixture/root/sub"}, {"/fixture/root/sub", "/fixture/root"}, {"/fixture/root", "/fixture/root"}} {
		if !forkPathsOverlap(p[0], p[1]) {
			t.Fatal("overlapping fork paths allowed")
		}
	}
	if forkPathsOverlap("/fixture/root", "/fixture/roots") {
		t.Fatal("disjoint sibling refused")
	}
}
