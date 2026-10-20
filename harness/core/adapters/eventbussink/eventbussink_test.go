package eventbussink

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	"github.com/hollis-labs/libs/mesh/eventbus"
)

type recorder struct{ items []eventbus.Item }

func (r *recorder) Publish(it eventbus.Item)    { r.items = append(r.items, it) }
func (r *recorder) Close(context.Context) error { return nil }

func event(kind runtimeevents.EventKind, class *runtimeevents.Class) runtimeevents.Event {
	now := time.Unix(10, 0).UTC()
	return runtimeevents.Event{
		SchemaVersion: runtimeevents.SchemaVersion, ID: runtimeevents.NewEventID(), Kind: kind, Time: now,
		Subject: "msg://resource/cerberus/tether", Sequence: 1, Source: runtimeevents.Source{Channel: "outbox"},
		Class: class, Cursor: "9", RecordedAt: &now,
		Actor: &runtimeevents.Actor{URN: "msg://service/x/y"},
	}
}

func body(t *testing.T, it eventbus.Item) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(it.Body, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestWriteClearsHubFieldsFillsAppAndKeepsID(t *testing.T) {
	r := &recorder{}
	if err := New(r, WithApp("cerberus")).Write(context.Background(), event("resource.state_changed", nil)); err != nil {
		t.Fatal(err)
	}
	if len(r.items) != 1 || r.items[0].ID == "" {
		t.Fatalf("items = %+v", r.items)
	}
	got := body(t, r.items[0])
	for _, k := range []string{"cursor", "recorded_at", "actor"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s must not be sent", k)
		}
	}
	if got["kind"] != "resource.state_changed" || got["schema_version"] != "1" || got["app"] != "cerberus" {
		t.Errorf("body = %v", got)
	}
}

func TestAnEventsOwnAppIsKept(t *testing.T) {
	r := &recorder{}
	ev := event("alert.raised", nil)
	ev.App = "torque"
	if err := New(r, WithApp("cerberus")).Write(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if got := body(t, r.items[0]); got["app"] != "torque" {
		t.Fatalf("app = %v", got["app"])
	}
}

// Everything the hub rejects on shape is refused to the producer at the call,
// and nothing is published.
func TestEnvelopesTheHubWouldRejectAreRefusedToTheProducer(t *testing.T) {
	withApp := func(p eventbus.Publisher) *Sink { return New(p, WithApp("cerberus")) }
	cases := map[string]struct {
		mutate func(*runtimeevents.Event)
		sink   func(eventbus.Publisher) *Sink
	}{
		"missing required field": {func(e *runtimeevents.Event) { e.Source.Channel = "" }, withApp},
		"non-canonical id":       {func(e *runtimeevents.Event) { e.ID = "evt_1" }, withApp},
		"unsupported version":    {func(e *runtimeevents.Event) { e.SchemaVersion = "999" }, withApp},
		"no app and no WithApp":  {func(e *runtimeevents.Event) {}, func(p eventbus.Publisher) *Sink { return New(p) }},
	}
	for name, c := range cases {
		r := &recorder{}
		ev := event("resource.state_changed", nil)
		c.mutate(&ev)
		if err := c.sink(r).Write(context.Background(), ev); err == nil {
			t.Errorf("%s: the producer must be told", name)
		}
		if len(r.items) != 0 {
			t.Errorf("%s: an event the hub would reject must not be published", name)
		}
	}
}

// Local means it never leaves the process: skipped, not an error, so a producer
// can write one stream to a local sink and the hub together.
func TestLocalEventsAreSkippedWithoutError(t *testing.T) {
	r := &recorder{}
	ev := event("resource.state_changed", &runtimeevents.Class{Visibility: "local"})
	sink := New(r, WithApp("cerberus"))
	if err := sink.Write(context.Background(), ev); err != nil {
		t.Fatalf("a local event is not an error: %v", err)
	}
	if len(r.items) != 0 {
		t.Fatal("a local event must never be published")
	}
	if got := sink.Stats().SkippedLocal; got != 1 {
		t.Fatalf("SkippedLocal = %d, want 1", got)
	}
	// A non-local event is published and not counted.
	if err := sink.Write(context.Background(), event("resource.state_changed", nil)); err != nil {
		t.Fatal(err)
	}
	if got := sink.Stats().SkippedLocal; got != 1 || len(r.items) != 1 {
		t.Fatalf("SkippedLocal = %d, published %d", got, len(r.items))
	}
}

func TestPriority(t *testing.T) {
	cases := []struct {
		name string
		ev   runtimeevents.Event
		want eventbus.Priority
	}{
		{"state from catalog", event("task.state_changed", nil), eventbus.PriorityHigh},
		{"activity from catalog", event("process.started", nil), eventbus.PriorityActivity},
		{"class activity on a non-protected kind", event("tool.call.started", &runtimeevents.Class{Category: "activity"}), eventbus.PriorityActivity},
		{"debug severity on a non-protected kind", event("process.started", &runtimeevents.Class{Severity: "debug"}), eventbus.PriorityDebug},
		// A kind the catalog does not know is activity unless its class says more.
		{"unknown kind defaults to activity", event("x.y", nil), eventbus.PriorityActivity},
		{"unknown kind with an explicit state class is protected", event("x.y", &runtimeevents.Class{Category: "state"}), eventbus.PriorityHigh},
		{"unknown kind with an explicit non-activity class is protected", event("x.y", &runtimeevents.Class{Category: "tool"}), eventbus.PriorityHigh},
		{"unknown kind at debug severity", event("x.y", &runtimeevents.Class{Severity: "debug"}), eventbus.PriorityDebug},
		{"alert", event("alert.raised", nil), eventbus.PriorityHigh},
		// Protected categories win over severity and over an explicit class: a
		// state change logged at debug is still a state change.
		{"debug severity does not downgrade state", event("task.state_changed", &runtimeevents.Class{Severity: "debug"}), eventbus.PriorityHigh},
		{"debug severity does not downgrade an alert", event("alert.raised", &runtimeevents.Class{Severity: "debug"}), eventbus.PriorityHigh},
		{"debug severity does not downgrade audit", event("audit.recorded", &runtimeevents.Class{Severity: "debug"}), eventbus.PriorityHigh},
		{"debug severity does not downgrade an approval", event("approval.requested", &runtimeevents.Class{Severity: "debug"}), eventbus.PriorityHigh},
		{"explicit activity class does not downgrade a catalog state", event("task.state_changed", &runtimeevents.Class{Category: "activity"}), eventbus.PriorityHigh},
		{"explicit state class protects a catalog activity kind", event("process.started", &runtimeevents.Class{Category: "state"}), eventbus.PriorityHigh},
	}
	for _, c := range cases {
		if got := priority(c.ev); got != c.want {
			t.Errorf("%s: priority = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNoopPublisher(t *testing.T) {
	if err := New(eventbus.Noop{}, WithApp("x")).Write(context.Background(), event("alert.raised", nil)); err != nil {
		t.Fatal(err)
	}
}

func TestGapMarkerIsAValidAlertEvent(t *testing.T) {
	first := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	loss := eventbus.Loss{Class: "protected", First: first, Last: first.Add(time.Minute), Count: 7}
	it, err := GapMarker("cerberus")(loss)
	if err != nil {
		t.Fatal(err)
	}
	if it.Priority != eventbus.PriorityHigh || it.ID == "" {
		t.Fatalf("item = %+v", it)
	}
	var ev runtimeevents.Event
	if err := json.Unmarshal(it.Body, &ev); err != nil {
		t.Fatal(err)
	}
	if err := ev.Validate(); err != nil {
		t.Fatalf("marker is not a valid envelope: %v", err)
	}
	if ev.Kind != "alert.raised" || ev.App != "cerberus" || ev.ID != it.ID {
		t.Fatalf("event = %+v", ev)
	}
	var p struct {
		RuleID     string   `json:"rule_id"`
		Sources    []string `json:"source_event_ids"`
		CoalesceBy string   `json:"coalesce_key"`
		Summary    string   `json:"summary"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.RuleID != "eventbus.loss" || p.Sources == nil || len(p.Sources) != 0 || p.CoalesceBy != "eventbus.loss:cerberus" || p.Summary == "" {
		t.Fatalf("payload = %+v", p)
	}
}

// The marker is identified within its producer as the contract asks: the
// sequence only goes up inside a generation, and both are stable per process.
func TestGapMarkerSequenceIsStrictlyMonotonicWithinAGeneration(t *testing.T) {
	first := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	mk := GapMarker("cerberus")
	var prevSeq, gen uint64
	for i := 0; i < 50; i++ {
		// A wall clock that goes backwards must not matter.
		last := first.Add(time.Duration(50-i) * time.Minute)
		it, err := mk(eventbus.Loss{Class: "protected", First: first, Last: last, Count: uint64(i + 1)})
		if err != nil {
			t.Fatal(err)
		}
		var ev runtimeevents.Event
		if err := json.Unmarshal(it.Body, &ev); err != nil {
			t.Fatal(err)
		}
		if i > 0 && ev.Sequence <= prevSeq {
			t.Fatalf("sequence went %d then %d", prevSeq, ev.Sequence)
		}
		if i > 0 && ev.Generation != gen {
			t.Fatalf("generation changed within a process: %d then %d", gen, ev.Generation)
		}
		prevSeq, gen = ev.Sequence, ev.Generation
	}
	if gen == 0 {
		t.Fatal("the marker must carry a generation")
	}
}

func TestGapMarkerRefusesAnEmptyApp(t *testing.T) {
	_, err := GapMarker("")(eventbus.Loss{Class: "protected", First: time.Now(), Last: time.Now(), Count: 1})
	if err == nil {
		t.Fatal("a marker the hub would reject (no app) must be an error, not an item")
	}
}

// Markers built from the same instant (a loss record with an unchanged
// timestamp) still get strictly increasing sequences, starting from 1 in a fresh
// process, on their own channel.
func TestGapMarkersFromTheSameInstantAreStrictlyIncreasing(t *testing.T) {
	at := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	loss := eventbus.Loss{Class: "protected", First: at, Last: at, Count: 1}
	mk := GapMarker("torque")
	var prev uint64
	for i := 0; i < 20; i++ {
		it, err := mk(loss)
		if err != nil {
			t.Fatal(err)
		}
		var ev runtimeevents.Event
		if err := json.Unmarshal(it.Body, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Source.Channel != "eventbus" {
			t.Fatalf("channel = %q, want its own channel", ev.Source.Channel)
		}
		if ev.Sequence <= prev {
			t.Fatalf("sequence %d did not increase past %d for markers built from the same instant", ev.Sequence, prev)
		}
		prev = ev.Sequence
	}
}
