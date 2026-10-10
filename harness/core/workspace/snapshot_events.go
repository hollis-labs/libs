package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/hollis-labs/substrate/harness/workspace/snapshot"
	"github.com/hollis-labs/substrate/mesh"
)

var (
	ErrSnapshotEventUnsupported = errors.New("workspace: snapshot event host unavailable")
	ErrSnapshotEventRefused     = errors.New("workspace: snapshot event admission refused")
	ErrSnapshotEventUncertain   = errors.New("workspace: snapshot event recording uncertain; retain obligations")
)

// SnapshotEventAdmission is host-observed source history, not a destination
// cursor or a capture capability. Reattach requires real replay through Journal
// before a held boundary can admit one coalesced observation.
type SnapshotEventAdmission struct {
	Journal          mesh.SnapshotJournalInterval
	Reason, Boundary string
	Coalesced        bool
	Uncaptured       *mesh.SnapshotJournalInterval
}

// SnapshotEventHost is an independently trusted host port. Implementations must
// refuse unavailable authority, source replay, custody or boundary support. The
// snapshot store lock is already held: acquisition must not recursively acquire
// it. No implementation or successful production admission is supplied here.
type SnapshotEventHost interface {
	AcquireSnapshotEvent(context.Context, snapshot.CaptureIntent) (SnapshotEventLease, error)
}

// SnapshotEventLease holds continuous external host authority and the admitted
// source boundary until Close. Admission returns detached observation data.
// Verify must check current principal, policy, binding, incarnation, controller
// epoch and replay admission against the exact capture intent.
type SnapshotEventLease interface {
	Admission() SnapshotEventAdmission
	Verify(context.Context, snapshot.CaptureIntent) error
	// Persist validates authority at the actual commit boundary, atomically with
	// durable event recording. It binds the operation/set and payload digest for
	// idempotency: uncertain attempts never become an automatic retry or another
	// capture. It owns the mesh envelope and returns its actual destination cursor.
	// The bytes contain only the bounded payload; no raw paths or provider errors.
	Persist(context.Context, snapshot.CaptureIntent, []byte) (string, error)
	Close() error
}

type SnapshotEventRecording struct {
	Cursor string
	// Attempted remains true even when persistence or later validation fails.
	// Such errors retain the journal pin and require host reconciliation.
	Attempted bool
}

// RecordSnapshotEvent never captures or releases retention references. It uses
// only a privately issued retained receipt and its verified durable manifest;
// diagnostic CaptureDescription data cannot enter this path. A missing host is
// disabled. Close releases locks while retaining the durable journal pin.
func RecordSnapshotEvent(ctx context.Context, receipt *snapshot.RetainedSet, operationID string, host SnapshotEventHost) (SnapshotEventRecording, error) {
	if host == nil || receipt == nil || operationID == "" {
		return SnapshotEventRecording{}, ErrSnapshotEventUnsupported
	}
	lease, err := receipt.Pin(ctx, operationID, snapshot.PinJournal)
	if err != nil {
		return SnapshotEventRecording{}, ErrSnapshotEventRefused
	}
	result, recordErr := recordSnapshotEvent(ctx, lease, operationID, host)
	if lease.Close() != nil {
		return result, ErrSnapshotEventUncertain
	}
	return result, recordErr
}

// The private interface permits ordering tests without any production receipt
// constructor. The public entry always supplies an actual held ReadLease.
type retainedSnapshotEventLease interface {
	Verify(context.Context) error
	Intent() snapshot.CaptureIntent
	Manifest() snapshot.RetainedManifest
}

func recordSnapshotEvent(ctx context.Context, retained retainedSnapshotEventLease, operationID string, host SnapshotEventHost) (result SnapshotEventRecording, returnedErr error) {
	if ctx.Err() != nil || retained.Verify(ctx) != nil {
		return result, ErrSnapshotEventRefused
	}
	intent := retained.Intent()
	if intent.OperationID != operationID {
		return result, ErrSnapshotEventRefused
	}
	fence, err := host.AcquireSnapshotEvent(ctx, intent)
	if err != nil || fence == nil {
		return result, ErrSnapshotEventRefused
	}
	defer func() {
		if fence.Close() != nil {
			returnedErr = ErrSnapshotEventUncertain
		}
	}()
	check := func() bool {
		return ctx.Err() == nil && retained.Verify(ctx) == nil && fence.Verify(ctx, intent) == nil && ctx.Err() == nil
	}
	if !check() {
		return result, ErrSnapshotEventRefused
	}
	admission := fence.Admission()
	payload := projectSnapshotManifest(intent, retained.Manifest(), admission)
	if payload.Validate() != nil {
		return result, ErrSnapshotEventRefused
	}
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) > mesh.MaxWorkspaceSnapshotPayloadBytes || !check() {
		return result, ErrSnapshotEventRefused
	}
	result.Attempted = true
	result.Cursor, err = fence.Persist(ctx, intent, encoded)
	// Revalidate after the external effect even on error. An actual commit may
	// have occurred; neither cancellation nor stale authority erases that fact.
	valid := check()
	if err != nil || result.Cursor == "" || !valid {
		return result, ErrSnapshotEventUncertain
	}
	return result, nil
}

func projectSnapshotManifest(i snapshot.CaptureIntent, m snapshot.RetainedManifest, a SnapshotEventAdmission) mesh.WorkspaceSnapshotTaken {
	p := mesh.WorkspaceSnapshotTaken{
		SchemaVersion: mesh.WorkspaceSnapshotPayloadV1,
		SetID:         i.SetID, OperationID: i.OperationID, RunID: i.RunID, InstanceID: i.InstanceID,
		InputDigest: i.InputDigest, TargetMapDigest: i.TargetMapDigest, PolicyRevision: i.PolicyRevision,
		BindingFence: i.BindingFence, ControllerEpoch: i.ControllerEpoch,
		BootGeneration: i.BootGeneration, RuntimeGeneration: i.RuntimeGeneration,
		Observation: mesh.SnapshotObservationInterval{StartedAt: m.Observation.StartedAt, FinishedAt: m.Observation.FinishedAt},
		Complete:    m.Complete, Journal: a.Journal, Reason: a.Reason, Boundary: a.Boundary, Coalesced: a.Coalesced,
	}
	if a.Uncaptured != nil {
		gap := *a.Uncaptured
		p.Uncaptured = &gap
	}
	for _, r := range m.Roots {
		root := mesh.SnapshotRootOutcome{RootID: r.RootID, StoreID: r.StoreID, TreeHash: r.TreeHash, CommitHash: r.CommitHash, ErrorCode: r.Code,
			Observation: mesh.SnapshotObservationInterval{StartedAt: r.Observation.StartedAt, FinishedAt: r.Observation.FinishedAt}}
		for _, s := range r.Skipped {
			root.Skipped = append(root.Skipped, mesh.SnapshotSkipped{Reason: s.Reason, Count: s.Count})
		}
		sort.Slice(root.Skipped, func(a, b int) bool { return root.Skipped[a].Reason < root.Skipped[b].Reason })
		p.Roots = append(p.Roots, root)
	}
	sort.Slice(p.Roots, func(a, b int) bool { return p.Roots[a].RootID < p.Roots[b].RootID })
	return p
}
