package trace_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/agent/sdk"
	"github.com/hollis-labs/libs/agent/sdk/internal/fakehost"
	"github.com/hollis-labs/libs/agent/sdk/trace"
	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	"github.com/hollis-labs/libs/llm/core/usageledger"
	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/gate/gatetest"
)

func recordActual(t *testing.T, unknown bool) (*trace.Recorder, *fakehost.Host) {
	t.Helper()
	h := fakehost.New()
	snapshot, build := gatetest.Reference("snapshot-1"), gatetest.Reference("build-1")
	r, err := trace.NewRecorder(trace.Metadata{Snapshot: &snapshot, Build: &build})
	if err != nil {
		t.Fatal(err)
	}
	p := h.Ports()
	p.Events = r
	h.ReceiptSink = r
	if unknown {
		p.Tools[0].Execute = func(context.Context, map[string]any) (sdk.ToolResult, error) {
			h.Effects++
			return sdk.ToolResult{}, errors.New("uncertain synthetic effect")
		}
	}
	o, err := sdk.New(fakehost.Definition(), p)
	if err != nil {
		t.Fatal(err)
	}
	_, err = o.Run(context.Background(), h.Input())
	if unknown && err == nil {
		t.Fatal("unknown effect did not fail")
	}
	if !unknown && err != nil {
		t.Fatal(err)
	}
	return r, h
}
func recording(t *testing.T, r *trace.Recorder) trace.Recording {
	t.Helper()
	s, err := r.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestRecordingIsDetachedAndUsesActualCorrelation(t *testing.T) {
	r, h := recordActual(t, false)
	s := recording(t, r)
	before, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	entries := s.Entries()
	if len(entries) == 0 {
		t.Fatal("missing actual events")
	}
	for _, e := range entries {
		if e.Correlation.Binding != h.Binding || e.Correlation.Environment != h.Environment.Receipt {
			t.Fatal("lost authority scope correlation")
		}
		if e.Event != nil && e.Event.TurnID != "" && e.Correlation.ModelCallID == "" {
			t.Fatal("missing model invocation ID")
		}
	}
	entries[0].Event.Payload[0] = '!'
	entries[0].Correlation.Binding.OwnerID = "other-owner"
	s.Metadata().Snapshot.ID = "mutated"
	after, _ := json.Marshal(s)
	if string(after) != string(before) {
		t.Fatal("getter mutation altered recording")
	}
	var imported trace.Recording
	if err = json.Unmarshal(before, &imported); err != nil {
		t.Fatal(err)
	}
	view, err := trace.Replay(context.Background(), imported)
	if err != nil {
		t.Fatal(err)
	}
	view[0].Event.Payload[0] = '!'
	again, _ := json.Marshal(imported)
	if string(again) != string(before) {
		t.Fatal("replay exposed mutable recording bytes")
	}
	if strings.Contains(string(before), h.Input().Text) || strings.Contains(string(before), "The value is 42") {
		t.Fatal("routine trace retained prompt/completion content")
	}
}
func TestDecisionDeliveryAndEffectRemainSeparate(t *testing.T) {
	r, h := recordActual(t, false)
	s := recording(t, r)
	var c trace.Correlation
	executions := 0
	for _, e := range s.Entries() {
		if e.Correlation.DecisionID != "" {
			c = e.Correlation
		}
		if e.Execution != nil {
			executions++
		}
		if e.Delivery != nil {
			t.Fatal("delivery inferred from decision")
		}
	}
	if c.DecisionID == "" {
		t.Fatal("missing actual decision ID")
	}
	native := gate.NativeRequestID(`9007199254740993`)
	delivery := gate.DeliveryReceipt{DecisionRef: string(c.DecisionID), Binding: h.Binding, AdapterID: "synthetic-native-adapter", NativeRequestID: native, Status: gate.DeliveryUnknown, EvidenceRef: "synthetic-disconnect"}
	if err := r.RecordDelivery(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
	native[0] = '1'
	entries, err := trace.Replay(context.Background(), recording(t, r))
	if err != nil {
		t.Fatal(err)
	}
	afterExecutions := 0
	deliveryUnknown := false
	for _, e := range entries {
		if e.Execution != nil {
			afterExecutions++
		}
		if e.Delivery != nil {
			deliveryUnknown = e.Delivery.Status == gate.DeliveryUnknown && string(e.Delivery.NativeRequestID) == `9007199254740993`
		}
	}
	if afterExecutions != executions || !deliveryUnknown {
		t.Fatal("delivery changed execution or lost native numeric ID")
	}
	forged := delivery
	forged.Binding.Generation++
	if err := r.RecordDelivery(context.Background(), forged); !errors.Is(err, trace.ErrInvalid) {
		t.Fatalf("stale delivery matched successor: %v", err)
	}
}
func TestSourcePositionsAndExistingTraceContextRemainDistinct(t *testing.T) {
	h := fakehost.New()
	p := sdk.EventPayload{RunID: "run-positions", Binding: h.Binding, Environment: h.Environment.Receipt, DefinitionDigest: "sha256:definition", Route: "fake", Model: "fake-model"}
	raw, _ := json.Marshal(p)
	offset := int64(0)
	event := runtimeevents.Event{SchemaVersion: runtimeevents.SchemaVersion, ID: "event-positions", Kind: "sdk.run.started", Time: h.Now(), SessionID: string(h.Binding.ConversationID), Sequence: 41, Generation: 3, RawOffset: &offset, Cursor: "committed-cursor-z", Source: runtimeevents.Source{Channel: "sdk-native"}, Payload: raw, Corr: &runtimeevents.Corr{TraceID: "host-trace", SpanID: "host-span", Traceparent: "host-traceparent"}}
	r, err := trace.NewRecorder(trace.Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Write(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	entries, err := trace.Replay(context.Background(), recording(t, r))
	if err != nil {
		t.Fatal(err)
	}
	e := entries[0]
	if e.Position.SourceSequence != 41 || e.Position.ProducerGeneration != 3 || e.Position.PTYOffset == nil || *e.Position.PTYOffset != 0 || e.Position.CommittedCursor != "committed-cursor-z" || e.Correlation.Binding.Generation != 7 || !reflect.DeepEqual(e.Event.Corr, event.Corr) {
		t.Fatalf("coordinate or trace context conflation: %+v", e)
	}
	event.Cursor = ""
	event.RawOffset = nil
	event.Sequence++
	if err = r.Write(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	last := recording(t, r).Entries()[1]
	if last.Position.CommittedCursor != "" || last.Position.PTYOffset != nil {
		t.Fatal("sequence invented replay position")
	}
}
func TestUnknownEffectReplayCannotActivateReplacementOrRepeatWork(t *testing.T) {
	r, old := recordActual(t, true)
	replacement := fakehost.New()
	replacement.Runs = old.Runs
	replacement.Binding.AttemptID = "replacement"
	replacement.Binding.Generation++
	p := replacement.Ports()
	p.Events = r
	replacement.ReceiptSink = r
	o, err := sdk.New(fakehost.Definition(), p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = o.Run(context.Background(), replacement.Input()); !errors.Is(err, sdk.ErrRunUsed) || replacement.ModelCalls != 0 || replacement.Effects != 0 {
		t.Fatalf("replacement reran uncertain work: %v", err)
	}
	entries, err := trace.Replay(context.Background(), recording(t, r))
	if err != nil {
		t.Fatal(err)
	}
	oldUnknown, newRefusal := false, false
	for _, e := range entries {
		if e.Execution != nil && e.Execution.State == gate.ExecutionUnknown {
			if e.Correlation.Binding != old.Binding {
				t.Fatal("old effect retargeted to successor")
			}
			oldUnknown = true
		}
		if e.Event != nil && e.Event.Kind == "sdk.run.failed" && e.Correlation.Binding == replacement.Binding {
			newRefusal = true
		}
	}
	if !oldUnknown || !newRefusal || old.Effects != 1 || old.ModelCalls != 1 || replacement.ModelCalls != 0 || replacement.Effects != 0 {
		t.Fatal("replay lost uncertainty or invoked replacement")
	}
}
func TestSupplementalUsageNeedsObservedCallAndRetainsUnknowns(t *testing.T) {
	r, h := recordActual(t, false)
	var c trace.Correlation
	for _, e := range recording(t, r).Entries() {
		if e.Event != nil && e.Event.Kind == runtimeevents.KindTurnStarted {
			c = e.Correlation
			break
		}
	}
	u := usageledger.NewUsage()
	u.OutputTokens = usageledger.Component{Tokens: 0, Provenance: usageledger.ProvenanceMeasured}
	row := usageledger.Row{SessionID: string(h.Binding.ConversationID), MessageID: string(c.ModelCallID), Provider: "synthetic", Model: string(c.Model), Usage: u, RecordedAt: h.Now()}
	forged := c
	forged.Binding.Generation++
	if err := r.RecordUsage(context.Background(), forged, row); !errors.Is(err, trace.ErrInvalid) {
		t.Fatalf("unobserved attempt self-certified usage: %v", err)
	}
	if err := r.RecordUsage(context.Background(), c, row); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordUsage(context.Background(), c, row); !errors.Is(err, trace.ErrInvalid) {
		t.Fatal("final usage double-counted")
	}
	entries, err := trace.Replay(context.Background(), recording(t, r))
	if err != nil {
		t.Fatal(err)
	}
	last := entries[len(entries)-1]
	if last.Usage == nil || last.Usage.Price != nil || last.Usage.Usage.OutputTokens.Provenance != usageledger.ProvenanceMeasured || last.Usage.Usage.UncachedInputTokens.Provenance != usageledger.ProvenanceUnknown {
		t.Fatal("usage presence changed")
	}
}
func TestImportedRecordingRejectsForgedCorrelationAndCancellation(t *testing.T) {
	r, _ := recordActual(t, false)
	s := recording(t, r)
	raw, _ := json.Marshal(s)
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	entries := d["entries"].([]any)
	entry := entries[0].(map[string]any)
	entry["correlation"].(map[string]any)["run_id"] = "forged-run"
	changed, _ := json.Marshal(d)
	var imported trace.Recording
	if err := json.Unmarshal(changed, &imported); err == nil {
		t.Fatal("raw envelope and projection mismatch accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := trace.Replay(ctx, s); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled replay continued")
	}
	if _, err := trace.Replay(context.Background(), trace.Recording{}); err == nil {
		t.Fatal("zero recording looked valid")
	}
}

func TestConcurrentRecordingAndSnapshotsKeepBothRunTrajectories(t *testing.T) {
	r, err := trace.NewRecorder(trace.Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 2)
	for _, id := range []string{"concurrent-a", "concurrent-b"} {
		h := fakehost.New()
		p := h.Ports()
		p.Events = r
		h.ReceiptSink = r
		o, err := sdk.New(fakehost.Definition(), p)
		if err != nil {
			t.Fatal(err)
		}
		in := h.Input()
		in.ID = id
		go func(o *sdk.Operative, in sdk.Input) { _, err := o.Run(context.Background(), in); finished <- err }(o, in)
	}
	// Snapshot/import while independent producers append, not after their locks
	// have already ceased to matter. A partial snapshot must still be coherent.
	partial, err := r.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = trace.Replay(context.Background(), partial); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = <-finished; err != nil {
			t.Fatal(err)
		}
	}
	observed := map[trace.RunID]bool{}
	for _, entry := range recording(t, r).Entries() {
		if entry.Execution != nil && entry.Execution.State == gate.ExecutionSucceeded {
			observed[entry.Correlation.RunID] = true
		}
	}
	if !observed["concurrent-a"] || !observed["concurrent-b"] {
		t.Fatal("concurrent recording lost a run trajectory")
	}
}
