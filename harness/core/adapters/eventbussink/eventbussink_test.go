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
		SchemaVersion: runtimeevents.SchemaVersion, ID: "evt_1", Kind: kind, Time: now,
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
	if len(r.items) != 1 || r.items[0].ID != "evt_1" {
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

func TestInvalidEventsAreRefusedToTheProducer(t *testing.T) {
	r := &recorder{}
	ev := event("alert.raised", nil)
	ev.Source.Channel = ""
	if err := New(r).Write(context.Background(), ev); err == nil {
		t.Fatal("an envelope the hub would reject must be reported to the producer")
	}
	if len(r.items) != 0 {
		t.Fatal("an invalid event must not be published")
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
		{"class activity", event("task.state_changed", &runtimeevents.Class{Category: "activity"}), eventbus.PriorityActivity},
		{"class state beats catalog activity", event("process.started", &runtimeevents.Class{Category: "state"}), eventbus.PriorityHigh},
		{"debug severity", event("task.state_changed", &runtimeevents.Class{Severity: "debug"}), eventbus.PriorityDebug},
		{"unknown kind", event("x.y", nil), eventbus.PriorityHigh},
		{"alert", event("alert.raised", nil), eventbus.PriorityHigh},
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
