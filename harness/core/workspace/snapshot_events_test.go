package workspace

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/harness/workspace/snapshot"
	"github.com/hollis-labs/substrate/mesh"
)

// These model the private coordinator's ordering, not production capture or
// custody. Actual issuance/manifest persistence is tested by the leaf owner.
type snapshotEventModel struct {
	intent                            snapshot.CaptureIntent
	manifest                          snapshot.RetainedManifest
	admission                         SnapshotEventAdmission
	trace                             []string
	invalidRetained, invalidFence     bool
	onAcquire, onAdmission, onPersist func()
	persistErr, acquireErr, closeErr  error
	encoded                           []byte
}

func eventModel() *snapshotEventModel {
	start := time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC)
	interval := snapshot.SnapshotInterval{StartedAt: start, FinishedAt: start.Add(time.Second)}
	return &snapshotEventModel{
		intent:    snapshot.CaptureIntent{SetID: "set", OperationID: "op", RunID: "run", InstanceID: "instance", InputDigest: strings.Repeat("a", 64), TargetMapDigest: strings.Repeat("b", 64), PolicyRevision: "policy", BindingFence: "binding", ControllerEpoch: 7, BootGeneration: "boot", RuntimeGeneration: "runtime"},
		manifest:  snapshot.RetainedManifest{Observation: interval, Complete: false, Roots: []snapshot.RetainedRootOutcome{{RootID: "root", StoreID: "store", TreeHash: strings.Repeat("a", 40), CommitHash: strings.Repeat("b", 40), Observation: interval, Skipped: []snapshot.RetainedSkip{{Reason: "oversize_untracked", Count: 1}}}}},
		admission: SnapshotEventAdmission{Journal: mesh.SnapshotJournalInterval{JournalID: "source", After: 10, Through: 20}, Reason: "reattach", Boundary: "quiescent", Coalesced: true, Uncaptured: &mesh.SnapshotJournalInterval{JournalID: "source", After: 10, Through: 20}},
	}
}

type retainedEventModel struct{ model *snapshotEventModel }

func (r retainedEventModel) Verify(context.Context) error {
	r.model.trace = append(r.model.trace, "retained")
	if r.model.invalidRetained {
		return errors.New("private store detail")
	}
	return nil
}
func (r retainedEventModel) Intent() snapshot.CaptureIntent      { return r.model.intent }
func (r retainedEventModel) Manifest() snapshot.RetainedManifest { return r.model.manifest }
func (m *snapshotEventModel) AcquireSnapshotEvent(context.Context, snapshot.CaptureIntent) (SnapshotEventLease, error) {
	m.trace = append(m.trace, "acquire")
	if m.onAcquire != nil {
		m.onAcquire()
	}
	return m, m.acquireErr
}
func (m *snapshotEventModel) Admission() SnapshotEventAdmission {
	m.trace = append(m.trace, "admission")
	if m.onAdmission != nil {
		m.onAdmission()
	}
	return m.admission
}
func (m *snapshotEventModel) Verify(context.Context, snapshot.CaptureIntent) error {
	m.trace = append(m.trace, "fence")
	if m.invalidFence {
		return errors.New("private authority detail")
	}
	return nil
}
func (m *snapshotEventModel) Persist(_ context.Context, _ snapshot.CaptureIntent, payload []byte) (string, error) {
	m.trace = append(m.trace, "persist")
	m.encoded = append([]byte(nil), payload...)
	if m.onPersist != nil {
		m.onPersist()
	}
	return "destination:99", m.persistErr
}
func (m *snapshotEventModel) Close() error { m.trace = append(m.trace, "close"); return m.closeErr }

func TestSnapshotEventModelClosesPartialAdmission(t *testing.T) {
	for _, closeFails := range []bool{false, true} {
		m := eventModel()
		m.acquireErr = errors.New("private partial admission failure")
		expected := ErrSnapshotEventRefused
		if closeFails {
			m.closeErr = errors.New("private lock release failure")
			expected = ErrSnapshotEventUncertain
		}
		result, err := recordSnapshotEvent(context.Background(), retainedEventModel{m}, "op", m)
		if !errors.Is(err, expected) || result.Attempted || strings.Join(m.trace, ",") != "retained,acquire,close" {
			t.Fatal("partial admission lost cleanup or reached persistence")
		}
	}
}

func TestSnapshotEventModelRecordsManifestAfterFenceChecks(t *testing.T) {
	m := eventModel()
	result, err := recordSnapshotEvent(context.Background(), retainedEventModel{m}, "op", m)
	if err != nil || !result.Attempted || result.Cursor != "destination:99" {
		t.Fatalf("recording: %+v %v", result, err)
	}
	payload, err := mesh.DecodeWorkspaceSnapshotTaken(m.encoded)
	if err != nil || payload.Complete || payload.Journal.Through != 20 || payload.Roots[0].Skipped[0].Count != 1 || !payload.Coalesced {
		t.Fatal("lost admitted incomplete manifest or source replay")
	}
	if strings.Join(m.trace, ",") != "retained,acquire,retained,fence,admission,retained,fence,persist,retained,fence,close" {
		t.Fatalf("effect ordering: %v", m.trace)
	}
}

func TestSnapshotEventModelRefusesAuthorityLostDuringAdmission(t *testing.T) {
	for _, boundary := range []string{"acquire", "admission"} {
		t.Run(boundary, func(t *testing.T) {
			m := eventModel()
			invalidate := func() { m.invalidFence = true }
			if boundary == "acquire" {
				m.onAcquire = invalidate
			} else {
				m.onAdmission = invalidate
			}
			r, err := recordSnapshotEvent(context.Background(), retainedEventModel{m}, "op", m)
			if !errors.Is(err, ErrSnapshotEventRefused) || r.Attempted || m.encoded != nil || m.trace[len(m.trace)-1] != "close" {
				t.Fatal("stale admission reached persistence or leaked lease")
			}
		})
	}
}

func TestSnapshotEventModelRetainsUncertaintyAfterPersistence(t *testing.T) {
	for _, cause := range []string{"cancel", "retained", "fence", "error"} {
		t.Run(cause, func(t *testing.T) {
			m := eventModel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m.onPersist = func() {
				switch cause {
				case "cancel":
					cancel()
				case "retained":
					m.invalidRetained = true
				case "fence":
					m.invalidFence = true
				case "error":
					m.persistErr = errors.New("secret raw failure")
				}
			}
			r, err := recordSnapshotEvent(ctx, retainedEventModel{m}, "op", m)
			if !errors.Is(err, ErrSnapshotEventUncertain) || !r.Attempted || r.Cursor != "destination:99" || strings.Contains(err.Error(), "secret") {
				t.Fatal("lost reached effect or leaked private error")
			}
			if m.trace[len(m.trace)-1] != "close" {
				t.Fatal("external lease leaked")
			}
		})
	}
}

func TestSnapshotEventModelCannotInventReplayOrCoverage(t *testing.T) {
	for _, cause := range []string{"journal", "gap", "complete", "operation"} {
		t.Run(cause, func(t *testing.T) {
			m := eventModel()
			switch cause {
			case "journal":
				m.admission.Journal.JournalID = ""
			case "gap":
				m.admission.Uncaptured = nil
			case "complete":
				m.manifest.Complete = true
			case "operation":
				m.intent.OperationID = "another"
			}
			r, err := recordSnapshotEvent(context.Background(), retainedEventModel{m}, "op", m)
			if !errors.Is(err, ErrSnapshotEventRefused) || r.Attempted || m.encoded != nil {
				t.Fatal("unsupported observation reached persistence")
			}
		})
	}
}

func TestSnapshotEventPublicEntryCannotUseDecodedReceipt(t *testing.T) {
	m := eventModel()
	if _, err := RecordSnapshotEvent(context.Background(), &snapshot.RetainedSet{}, "op", m); !errors.Is(err, ErrSnapshotEventRefused) {
		t.Fatal("unissued receipt admitted")
	}
	if len(m.trace) != 0 {
		t.Fatal("unissued receipt reached host")
	}
	if _, err := RecordSnapshotEvent(context.Background(), nil, "op", nil); !errors.Is(err, ErrSnapshotEventUnsupported) {
		t.Fatal("missing port enabled")
	}
}
