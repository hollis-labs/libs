package mesh_test

import (
	"encoding/json"
	mesh "github.com/hollis-labs/substrate/mesh"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func snapshotFixture(t *testing.T) ([]byte, mesh.WorkspaceSnapshotTaken) {
	t.Helper()
	raw, err := os.ReadFile("testdata/workspace_snapshot_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := mesh.DecodeWorkspaceSnapshotTaken(raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw, p
}

func TestWorkspaceSnapshotPreservesPerRootIntervalsAndFailure(t *testing.T) {
	_, p := snapshotFixture(t)
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := mesh.DecodeWorkspaceSnapshotTaken(raw)
	if err != nil || !reflect.DeepEqual(p, got) {
		t.Fatalf("roundtrip err=%v", err)
	}
	if got.Roots[0].Observation.FinishedAt.Equal(got.Roots[1].Observation.FinishedAt) || got.Complete || got.Roots[1].ErrorCode != "capture_failed" || got.Roots[1].CommitHash != "" {
		t.Fatal("cross-root atomicity or success fabricated")
	}
}

func TestWorkspaceSnapshotRejectsFalseCoverageAndUnboundIntervals(t *testing.T) {
	cases := map[string]func(*mesh.WorkspaceSnapshotTaken){
		"unknown version":            func(p *mesh.WorkspaceSnapshotTaken) { p.SchemaVersion = "workspace.snapshot.taken.v2" },
		"duplicate root":             func(p *mesh.WorkspaceSnapshotTaken) { p.Roots[1].RootID = p.Roots[0].RootID },
		"failed root complete":       func(p *mesh.WorkspaceSnapshotTaken) { p.Complete = true },
		"failed root synthetic hash": func(p *mesh.WorkspaceSnapshotTaken) { p.Roots[1].TreeHash = p.Roots[0].TreeHash },
		"reversed observation": func(p *mesh.WorkspaceSnapshotTaken) {
			p.Observation.StartedAt = p.Observation.FinishedAt.Add(time.Second)
		},
		"root outside observation": func(p *mesh.WorkspaceSnapshotTaken) {
			p.Roots[0].Observation.StartedAt = p.Observation.StartedAt.Add(-time.Second)
		},
		"reversed source cursor": func(p *mesh.WorkspaceSnapshotTaken) { p.Journal.After = p.Journal.Through + 1 },
		"foreign journal gap":    func(p *mesh.WorkspaceSnapshotTaken) { p.Uncaptured.JournalID = "another-journal" },
		"unreplayed gap":         func(p *mesh.WorkspaceSnapshotTaken) { p.Uncaptured.Through = p.Journal.Through + 1 },
		"missing coalesced gap":  func(p *mesh.WorkspaceSnapshotTaken) { p.Uncaptured = nil },
		"gap without coalescing": func(p *mesh.WorkspaceSnapshotTaken) { p.Coalesced = false },
		"unknown fence":          func(p *mesh.WorkspaceSnapshotTaken) { p.BindingFence = "" },
		"zero epoch":             func(p *mesh.WorkspaceSnapshotTaken) { p.ControllerEpoch = 0 },
		"raw error":              func(p *mesh.WorkspaceSnapshotTaken) { p.Roots[1].ErrorCode = "git error containing private input" },
		"zero skipped count":     func(p *mesh.WorkspaceSnapshotTaken) { p.Roots[0].Skipped[0].Count = 0 },
		"duplicate skipped reason": func(p *mesh.WorkspaceSnapshotTaken) {
			p.Roots[0].Skipped = append(p.Roots[0].Skipped, p.Roots[0].Skipped[0])
		},
		"unsupported reason": func(p *mesh.WorkspaceSnapshotTaken) { p.Reason = "shim_automatic" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			_, p := snapshotFixture(t)
			change(&p)
			if p.Validate() == nil {
				t.Fatal("accepted invalid payload")
			}
		})
	}
}

func TestWorkspaceSnapshotStrictWireRefusals(t *testing.T) {
	raw, _ := snapshotFixture(t)
	cases := []string{
		strings.Replace(string(raw), `"controller_epoch":9`, `"controller_epoch":9,"controller_epoch":10`, 1),
		strings.Replace(string(raw), `"controller_epoch":9`, `"controller_epoch":9,"Controller_Epoch":10`, 1),
		strings.Replace(string(raw), `"controller_epoch":9`, `"controller_epoch":18446744073709551616`, 1),
		strings.Replace(string(raw), `"store_id":"store-1"`, `"store_id":"store-1","path":"private-path"`, 1),
		string(raw) + `{}`,
	}
	for _, bad := range cases {
		if _, err := mesh.DecodeWorkspaceSnapshotTaken([]byte(bad)); err == nil {
			t.Fatal("accepted unsafe wire shape")
		}
	}
}

func TestWorkspaceSnapshotEnvelopeKeepsDestinationCursorIndependent(t *testing.T) {
	raw, p := snapshotFixture(t)
	e := mesh.Event{SchemaVersion: "1", ID: "event-1", Kind: mesh.WorkspaceSnapshotTakenKind, Time: p.Observation.FinishedAt, SessionID: p.RunID, Source: mesh.EventSource{Channel: "host"}, Actor: mesh.Actor{URN: "msg://service/test/host", Kind: mesh.ActorService}, Subject: "msg://session/test/run", CausationID: p.OperationID, CorrelationID: p.SetID, Generation: 77, SourceSequence: 99, Cursor: "destination-store-1000", ContentType: "application/json", PayloadSchema: mesh.WorkspaceSnapshotPayloadV1, Visibility: "private", Payload: raw}
	if err := mesh.ValidateWorkspaceSnapshotEvent(e); err != nil {
		t.Fatal(err)
	}
	// These are envelope producer counters, not controller epoch or source cursor.
	e.Generation = 78
	e.SourceSequence = 100
	e.Cursor = "destination-store-2000"
	if err := mesh.ValidateWorkspaceSnapshotEvent(e); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*mesh.Event){
		"wrong set":         func(e *mesh.Event) { e.CorrelationID = "another-set" },
		"wrong run":         func(e *mesh.Event) { e.SessionID = "another-run" },
		"before capture":    func(e *mesh.Event) { e.Time = p.Observation.FinishedAt.Add(-time.Second) },
		"public metadata":   func(e *mesh.Event) { e.Visibility = "public" },
		"truncated outcome": func(e *mesh.Event) { e.Truncated = true },
	} {
		t.Run(name, func(t *testing.T) {
			copy := e
			change(&copy)
			if mesh.ValidateWorkspaceSnapshotEvent(copy) == nil {
				t.Fatal("accepted unbound envelope")
			}
		})
	}
}

func TestWorkspaceSnapshotLegitimateInitialAndCompleteObservation(t *testing.T) {
	_, p := snapshotFixture(t)
	p.Reason = "run_start"
	p.Coalesced = false
	p.Uncaptured = nil
	p.Journal.After = 0
	p.Journal.Through = 0
	p.Roots = p.Roots[:1]
	p.Roots[0].Skipped = []mesh.SnapshotSkipped{}
	p.Complete = true
	p.Roots[0].Skipped = []mesh.SnapshotSkipped{{Reason: "git_metadata", Count: 1}, {Reason: "excluded", Count: 2}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.Roots[0].Skipped = append(p.Roots[0].Skipped, mesh.SnapshotSkipped{Reason: "oversize_untracked", Count: 1})
	if p.Validate() == nil {
		t.Fatal("oversize omission claimed complete eligible scope")
	}
	p.Roots[0].Skipped = p.Roots[0].Skipped[:2]
	p.Boundary = "best_effort"
	if p.Validate() == nil {
		t.Fatal("best-effort observation claimed complete cut")
	}
	p.Complete = false
	if err := p.Validate(); err != nil {
		t.Fatal("honest best effort refused", err)
	}
}
