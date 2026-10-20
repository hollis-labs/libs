package ingest

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
)

func envelopeEvent() runtimeevents.Event {
	return runtimeevents.Event{
		SchemaVersion: runtimeevents.SchemaVersion,
		ID:            runtimeevents.NewEventID(),
		Kind:          "resource.state_changed",
		Time:          time.Now().UTC(),
		App:           "cerberus",
		Subject:       "msg://resource/cerberus/example",
		Sequence:      1,
		Source:        runtimeevents.Source{Channel: "outbox"},
	}
}

func marshalEvent(t *testing.T, ev runtimeevents.Event) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// CheckEnvelope is the shape check alone: it does not know who may publish what.
func TestCheckEnvelopeIsShapeOnly(t *testing.T) {
	if _, rej := CheckEnvelope(marshalEvent(t, envelopeEvent())); rej != nil {
		t.Fatalf("a well-formed event was rejected: %v", rej)
	}
	// An app that does not own the kind, and a kind the catalog does not know,
	// are the hub's allowlist decisions, not shape problems.
	ev := envelopeEvent()
	ev.App = "tangent"
	if _, rej := CheckEnvelope(marshalEvent(t, ev)); rej != nil {
		t.Fatalf("CheckEnvelope must not apply the producer allowlist: %v", rej)
	}
	ev = envelopeEvent()
	ev.Kind = "made.up_kind"
	if _, rej := CheckEnvelope(marshalEvent(t, ev)); rej != nil {
		t.Fatalf("CheckEnvelope must not consult the catalog: %v", rej)
	}
	// ... which CheckItem still does.
	if _, rej := CheckItem("cerberus", marshalEvent(t, ev), Options{}); rej == nil || rej.Code != CodeUnknownKind {
		t.Fatalf("CheckItem must still reject an unknown kind: %v", rej)
	}
	if _, rej := CheckItem("tangent", marshalEvent(t, envelopeEvent()), Options{}); rej == nil || rej.Code != CodeForbiddenKind {
		t.Fatalf("CheckItem must still enforce ownership: %v", rej)
	}
}

func TestCheckEnvelopeRejectsShapeProblems(t *testing.T) {
	cases := map[string]func(*runtimeevents.Event){
		"non-canonical id":    func(e *runtimeevents.Event) { e.ID = "evt_1" },
		"unsupported version": func(e *runtimeevents.Event) { e.SchemaVersion = "999" },
		"no app":              func(e *runtimeevents.Event) { e.App = "" },
		"no channel":          func(e *runtimeevents.Event) { e.Source.Channel = "" },
		"local visibility":    func(e *runtimeevents.Event) { e.Class = &runtimeevents.Class{Visibility: "local"} },
	}
	for name, mutate := range cases {
		ev := envelopeEvent()
		mutate(&ev)
		if _, rej := CheckEnvelope(marshalEvent(t, ev)); rej == nil || rej.Code != CodeSchemaInvalid {
			t.Errorf("%s: got %v, want schema_invalid", name, rej)
		}
	}
	if _, rej := CheckEnvelope(json.RawMessage(`not json`)); rej == nil {
		t.Error("garbage must be rejected")
	}
}
