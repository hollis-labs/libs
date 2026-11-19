package evidence_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/libs/agent/trace/evidence"
	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	"github.com/hollis-labs/libs/llm/core/usageledger"
	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/gate/gatetest"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

var binding = runtimecontract.Binding{OperativeID: "operative-1", SeatID: "seat-1", ConversationID: "conversation-1", OwnerID: "owner-1", AttemptID: "attempt-1", Generation: 1}

// producer emits evidence payloads for one run, as any agent producer would.
type producer struct {
	emitter runtimeevents.Emitter
	base    evidence.EventPayload
}

func newProducer(sink runtimeevents.Sink, run string) *producer {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	return &producer{
		emitter: runtimeevents.Emitter{Sink: sink, App: "fixture", SessionID: string(binding.ConversationID), Sequencer: runtimeevents.NewSequencer(), Now: func() time.Time { return now }},
		base:    evidence.EventPayload{RunID: run, Binding: binding, Environment: gatetest.Reference("environment-1"), DefinitionDigest: "sha256:definition", Route: "default", Model: "model-1"},
	}
}

func (p *producer) emit(t *testing.T, kind runtimeevents.EventKind, turn string, extra func(*evidence.EventPayload)) {
	t.Helper()
	payload := p.base
	payload.TurnID = runtimecontract.TurnID(turn)
	if turn != "" {
		payload.ModelCallID = evidence.ModelCallID(turn + "/model/1")
	}
	if extra != nil {
		extra(&payload)
	}
	if err := p.emitter.Emit(context.Background(), kind, runtimeevents.Source{Channel: "fixture", Confidence: runtimeevents.ConfidenceExact}, payload, runtimeevents.WithTurnID(turn)); err != nil {
		t.Error(err) // Error, not Fatal: producers also run on other goroutines.
	}
}

func record(t *testing.T) (*evidence.Recorder, evidence.Correlation) {
	t.Helper()
	r, err := evidence.NewRecorder(evidence.Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	p := newProducer(r, "run-1")
	p.emit(t, runtimeevents.KindTurnStarted, "turn-1", nil)
	p.emit(t, runtimeevents.KindAgentToolUse, "turn-1", func(e *evidence.EventPayload) {
		e.Tool, e.ToolCallID, e.OperationID, e.DecisionID = "read_value", "call-1", "operation-1", "decision-1"
	})
	p.emit(t, runtimeevents.KindTurnCompleted, "turn-1", func(e *evidence.EventPayload) { e.Status = evidence.Succeeded })
	s, err := r.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return r, s.Entries()[0].Correlation
}

func TestRecordExportImportReplay(t *testing.T) {
	r, c := record(t)
	s, err := r.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Entries()) != 3 || c.RunID != "run-1" || c.ModelCallID != "turn-1/model/1" || c.Binding != binding {
		t.Fatalf("entries %+v", s.Entries())
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var imported evidence.Recording
	if err := json.Unmarshal(raw, &imported); err != nil {
		t.Fatal(err)
	}
	entries, err := evidence.Replay(context.Background(), imported)
	if err != nil || len(entries) != 3 {
		t.Fatalf("replay %d, %v", len(entries), err)
	}
	entries[0].Event.Payload[0] = '!'
	again, _ := json.Marshal(imported)
	if string(again) != string(raw) {
		t.Fatal("replay exposed mutable recording bytes")
	}
}

func TestUnknownPayloadFieldIsRejected(t *testing.T) {
	r, err := evidence.NewRecorder(evidence.Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	// Capture one valid event, then add a field the schema does not know.
	var captured runtimeevents.Event
	p := newProducer(runtimeevents.SinkFunc(func(_ context.Context, e runtimeevents.Event) error { captured = e; return nil }), "run-1")
	p.emit(t, runtimeevents.KindTurnStarted, "turn-1", nil)
	if err := r.Write(context.Background(), captured); err != nil {
		t.Fatalf("the valid event was refused: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(captured.Payload, &fields); err != nil {
		t.Fatal(err)
	}
	fields["prompt"] = "secret text"
	captured.Payload, _ = json.Marshal(fields)
	captured.ID = "evt-unknown-field"
	if err := r.Write(context.Background(), captured); err == nil {
		t.Fatal("an unknown payload field was accepted")
	}
}

func TestSupplementalEvidenceNeedsAnObservedCorrelation(t *testing.T) {
	r, c := record(t)
	u := usageledger.NewUsage()
	u.OutputTokens = usageledger.Component{Tokens: 12, Provenance: usageledger.ProvenanceMeasured}
	row := usageledger.Row{SessionID: string(binding.ConversationID), MessageID: string(c.ModelCallID), Provider: "fixture", Model: string(c.Model), Usage: u, RecordedAt: time.Now()}
	forged := c
	forged.Binding.Generation++
	if err := r.RecordUsage(context.Background(), forged, row); !errors.Is(err, evidence.ErrInvalid) {
		t.Fatalf("usage for an unobserved attempt: %v", err)
	}
	if err := r.RecordUsage(context.Background(), c, row); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordUsage(context.Background(), c, row); !errors.Is(err, evidence.ErrInvalid) {
		t.Fatal("final usage double-counted")
	}
	delivery := gate.DeliveryReceipt{DecisionRef: "decision-unknown", Binding: binding, AdapterID: "fixture", NativeRequestID: gate.NativeRequestID(`1`), Status: gate.DeliveryUnknown, EvidenceRef: "fixture"}
	if err := r.RecordDelivery(context.Background(), delivery); !errors.Is(err, evidence.ErrInvalid) {
		t.Fatalf("delivery without an observed decision: %v", err)
	}
	delivery.DecisionRef = "decision-1"
	if err := r.RecordDelivery(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
}

func TestForgedImportAndCancellation(t *testing.T) {
	r, _ := record(t)
	s, _ := r.Snapshot()
	raw, _ := json.Marshal(s)
	forged := strings.Replace(string(raw), `"run_id":"run-1"`, `"run_id":"forged"`, 1)
	var imported evidence.Recording
	if err := json.Unmarshal([]byte(forged), &imported); err == nil {
		t.Fatal("a projection that disagrees with its envelope was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := evidence.Replay(ctx, s); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled replay continued")
	}
	if _, err := evidence.Replay(context.Background(), evidence.Recording{}); err == nil {
		t.Fatal("zero recording looked valid")
	}
}

func TestConcurrentProducers(t *testing.T) {
	r, err := evidence.NewRecorder(evidence.Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, run := range []string{"run-a", "run-b"} {
		wg.Go(func() {
			p := newProducer(r, run)
			for range 20 {
				p.emit(t, runtimeevents.KindAgentDelta, "turn-"+run, nil)
			}
		})
	}
	wg.Wait()
	s, err := r.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evidence.Replay(context.Background(), s); err != nil || len(s.Entries()) != 40 {
		t.Fatalf("entries %d, %v", len(s.Entries()), err)
	}
}
