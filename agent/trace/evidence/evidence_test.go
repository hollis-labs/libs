package evidence_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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

func TestDecisionDeliveryAndExecutionRemainSeparate(t *testing.T) {
	r, c := record(t)
	s, _ := r.Snapshot()
	for _, e := range s.Entries() {
		if e.Delivery != nil || e.Execution != nil {
			t.Fatal("a receipt was inferred from a decision")
		}
	}
	native := gate.NativeRequestID(`9007199254740993`)
	delivery := gate.DeliveryReceipt{DecisionRef: "decision-1", Binding: binding, AdapterID: "fixture", NativeRequestID: native, Status: gate.DeliveryUnknown, EvidenceRef: "fixture"}
	if err := r.RecordDelivery(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
	execution := gate.ExecutionReceipt{DecisionRef: "decision-1", OperationID: "operation-1", Binding: binding, State: gate.ExecutionSucceeded, EvidenceRef: "fixture"}
	forged := execution
	forged.Binding.Generation++
	if err := r.RecordExecution(context.Background(), forged); !errors.Is(err, evidence.ErrInvalid) {
		t.Fatalf("execution for a successor attempt: %v", err)
	}
	unobserved := execution
	unobserved.OperationID = "operation-unknown"
	if err := r.RecordExecution(context.Background(), unobserved); !errors.Is(err, evidence.ErrInvalid) {
		t.Fatalf("execution for an unobserved operation: %v", err)
	}
	if err := r.RecordExecution(context.Background(), execution); err != nil {
		t.Fatal(err)
	}
	s, _ = r.Snapshot()
	entries, err := evidence.Replay(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	deliveries, executions := 0, 0
	for _, e := range entries {
		if e.Delivery != nil {
			deliveries++
			if string(e.Delivery.NativeRequestID) != `9007199254740993` || e.Delivery.Status != gate.DeliveryUnknown {
				t.Fatalf("delivery changed in replay: %+v", e.Delivery)
			}
		}
		if e.Execution != nil {
			executions++
			if e.Correlation != c && e.Correlation.OperationID != "operation-1" {
				t.Fatalf("execution correlation %+v", e.Correlation)
			}
		}
	}
	if deliveries != 1 || executions != 1 {
		t.Fatalf("deliveries %d, executions %d", deliveries, executions)
	}
}

func TestPositionsAndTraceContextRemainDistinct(t *testing.T) {
	var captured runtimeevents.Event
	p := newProducer(runtimeevents.SinkFunc(func(_ context.Context, e runtimeevents.Event) error { captured = e; return nil }), "run-positions")
	p.emit(t, runtimeevents.KindTurnStarted, "turn-1", nil)
	offset := int64(0)
	captured.Sequence, captured.Generation, captured.RawOffset, captured.Cursor = 41, 3, &offset, "committed-cursor-z"
	captured.Corr = &runtimeevents.Corr{TraceID: "host-trace", SpanID: "host-span", Traceparent: "host-traceparent"}
	r, err := evidence.NewRecorder(evidence.Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Write(context.Background(), captured); err != nil {
		t.Fatal(err)
	}
	s, _ := r.Snapshot()
	entries, err := evidence.Replay(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	e := entries[0]
	if e.Position.SourceSequence != 41 || e.Position.ProducerGeneration != 3 || e.Position.PTYOffset == nil || *e.Position.PTYOffset != 0 ||
		e.Position.CommittedCursor != "committed-cursor-z" || e.Correlation.Binding.Generation != binding.Generation || !reflect.DeepEqual(e.Event.Corr, captured.Corr) {
		t.Fatalf("coordinates or trace context conflated: %+v", e)
	}
	captured.ID, captured.Cursor, captured.RawOffset = "evt-next", "", nil
	captured.Sequence++
	if err := r.Write(context.Background(), captured); err != nil {
		t.Fatal(err)
	}
	s, _ = r.Snapshot()
	if last := s.Entries()[1]; last.Position.CommittedCursor != "" || last.Position.PTYOffset != nil {
		t.Fatal("a sequence invented a replay position")
	}
}

func TestImportRejectsUnknownFieldsAndTheEntryBound(t *testing.T) {
	r, _ := record(t)
	s, _ := r.Snapshot()
	raw, _ := json.Marshal(s)
	extra := strings.Replace(string(raw), `{"version":`, `{"unexpected":true,"version":`, 1)
	var imported evidence.Recording
	if err := json.Unmarshal([]byte(extra), &imported); err == nil {
		t.Fatal("an unknown recording field was accepted")
	}
	big, err := evidence.NewRecorder(evidence.Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	p := newProducer(big, "run-big")
	for range evidence.MaxEntries {
		p.emit(t, runtimeevents.KindAgentDelta, "turn-1", nil)
	}
	if t.Failed() {
		t.FailNow()
	}
	payload := p.base
	payload.TurnID, payload.ModelCallID = "turn-1", "turn-1/model/1"
	err = p.emitter.Emit(context.Background(), runtimeevents.KindAgentDelta, runtimeevents.Source{Channel: "fixture", Confidence: runtimeevents.ConfidenceExact}, payload, runtimeevents.WithTurnID("turn-1"))
	if !errors.Is(err, evidence.ErrInvalid) {
		t.Fatalf("entry %d was accepted: %v", evidence.MaxEntries+1, err)
	}
}
