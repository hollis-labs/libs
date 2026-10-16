package eventstage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	mesh "github.com/hollis-labs/libs/mesh/core"
)

// fakePayload redacts SECRET and reports it; "TOOBIG" fails with a class.
type fakePayload struct{ sensitivity string }

func (f fakePayload) SanitizePayload(_ context.Context, _ string, p []byte) ([]byte, PayloadReport, error) {
	s := string(p)
	if strings.Contains(s, "TOOBIG") {
		return nil, PayloadReport{ErrorClass: "oversized"}, nil
	}
	if strings.Contains(s, "EXPLODE") {
		return nil, PayloadReport{}, errors.New("boom with SECRET inside")
	}
	n := strings.Count(s, "SECRET")
	return []byte(strings.ReplaceAll(s, "SECRET", "[redacted]")), PayloadReport{
		Redactions: uint32(n), Sensitivity: f.sensitivity, PII: strings.Contains(s, "PII"),
	}, nil
}

func rtEvent(payload string) *runtimeevents.Event {
	return &runtimeevents.Event{SchemaVersion: "1", ID: "evt_1", Kind: "message.sent", Time: time.Unix(1, 0),
		Subject: "msg://x", Sequence: 1, Payload: json.RawMessage(payload)}
}

func TestRuntimeEventsSanitizeSetsTheClassBlock(t *testing.T) {
	st := RuntimeEventsSanitize(fakePayload{sensitivity: "secret"}, ModeProducer)
	e := rtEvent(`{"a":"SECRET","b":"PII"}`)
	out, err := st.Apply(t.Context(), e)
	if err != nil || out.Note != NoteSanitized {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if strings.Contains(string(e.Payload), "SECRET") || !e.Class.Sanitized || e.Class.Redactions != 1 || e.Class.Sensitivity != "secret" || !e.Class.PII {
		t.Fatalf("event = %s class=%+v", e.Payload, e.Class)
	}
}

func TestRuntimeEventsSanitizeFailsClosedKeepingTheEnvelope(t *testing.T) {
	c, _ := NewChain(Config[runtimeevents.Event]{Sanitize: RuntimeEventsSanitize(fakePayload{}, ModeProducer)})
	for name, payload := range map[string]string{"oversized": `{"x":"TOOBIG SECRET"}`, "error": `{"x":"EXPLODE SECRET"}`} {
		e := rtEvent(payload)
		rep := c.Apply(t.Context(), e)
		if !rep.Emit() || string(e.Payload) != `{"sanitize_error":"`+name+`"}` || e.Class.Sanitized {
			t.Fatalf("%s: emit=%v payload=%s class=%+v", name, rep.Emit(), e.Payload, e.Class)
		}
		if e.ID != "evt_1" || e.Kind != "message.sent" || e.Subject != "msg://x" {
			t.Fatalf("%s: envelope changed: %+v", name, e)
		}
		if strings.Contains(string(e.Payload), "SECRET") || strings.Contains(rep.Err().Error(), "SECRET") {
			t.Fatalf("%s: value leaked", name)
		}
	}
}

func TestErrorClassIsReducedToASafeToken(t *testing.T) {
	if got := cleanClass("Bad Class/with SECRET!"); strings.ContainsAny(got, " /!A-Z") || len(got) > maxErrorClass {
		t.Fatalf("class = %q", got)
	}
	if cleanClass("") != "error" {
		t.Fatal("empty class must default")
	}
}

func TestMeshSanitizeAndTheHubBackstop(t *testing.T) {
	e := &mesh.Event{SchemaVersion: "1", ID: "evt_1", Kind: "message.sent", Payload: json.RawMessage(`{"a":"SECRET"}`)}
	producer := MeshSanitize(fakePayload{sensitivity: "private"}, ModeProducer)
	if out, err := producer.Apply(t.Context(), e); err != nil || out.Note != NoteSanitized || e.Class.Redactions != 1 {
		t.Fatalf("producer pass: out=%+v err=%v class=%+v", out, err, e.Class)
	}
	// A clean, already-sanitized event passes the backstop quietly...
	hub := MeshSanitize(fakePayload{sensitivity: "secret"}, ModeVerifyAndScrub)
	if out, _ := hub.Apply(t.Context(), e); out.Note != NoteSanitized || e.Class.Redactions != 1 {
		t.Fatalf("clean backstop pass: out=%+v class=%+v", out, e.Class)
	}
	// ...one that still needed scrubbing adds to redactions and says so.
	e.Payload = json.RawMessage(`{"a":"SECRET"}`)
	out, _ := hub.Apply(t.Context(), e)
	if out.Note != NoteBackstopScrubbed || e.Class.Redactions != 2 || e.Class.Sensitivity != "secret" || strings.Contains(string(e.Payload), "SECRET") {
		t.Fatalf("backstop: out=%+v payload=%s class=%+v", out, e.Payload, e.Class)
	}
}

func TestSensitivityNeverDowngrades(t *testing.T) {
	e := rtEvent(`{"a":1}`)
	e.Class = &runtimeevents.Class{Sensitivity: "secret"}
	if _, err := RuntimeEventsSanitize(fakePayload{sensitivity: "public"}, ModeProducer).Apply(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if e.Class.Sensitivity != "secret" {
		t.Fatalf("sensitivity = %q", e.Class.Sensitivity)
	}
	if moreRestrictive("secret", "weird") != "weird" {
		t.Fatal("an unknown class must outrank the known ones")
	}
}

func TestEmptyPayloadIsMarkedSanitizedWithoutCallingTheSanitizer(t *testing.T) {
	for _, p := range []string{"", "null"} {
		e := rtEvent(p)
		out, err := RuntimeEventsSanitize(panicSanitizer{}, ModeProducer).Apply(t.Context(), e)
		if err != nil || out.Note != NoteNoPayload || !e.Class.Sanitized {
			t.Fatalf("%q: out=%+v err=%v", p, out, err)
		}
	}
}

type panicSanitizer struct{}

func (panicSanitizer) SanitizePayload(context.Context, string, []byte) ([]byte, PayloadReport, error) {
	panic("must not be called")
}

func TestBackstopTellsAnUnsanitizedEventFromOneThatFailedClosed(t *testing.T) {
	hub := MeshSanitize(fakePayload{}, ModeVerifyAndScrub)

	skipped := &mesh.Event{Kind: "message.sent", Payload: json.RawMessage(`{"a":"SECRET"}`)}
	if out, _ := hub.Apply(t.Context(), skipped); out.Note != NoteBackstopUnsanitized || strings.Contains(string(skipped.Payload), "SECRET") {
		t.Fatalf("producer that never sanitized: out=%+v payload=%s", out, skipped.Payload)
	}

	// A fail-closed event keeps its payload shape and sanitized=false.
	failed := &mesh.Event{Kind: "message.sent", Payload: ErrorPayload("oversized"), Class: &mesh.EventClass{Redactions: 2}}
	out, err := hub.Apply(t.Context(), failed)
	if err != nil || out.Note != NoteBackstopFailedClosed || out.Changed {
		t.Fatalf("event that failed closed upstream: out=%+v err=%v", out, err)
	}
	if failed.Class.Sanitized || string(failed.Payload) != string(ErrorPayload("oversized")) || failed.Class.Redactions != 2 {
		t.Fatalf("backstop altered a fail-closed event: payload=%s class=%+v", failed.Payload, failed.Class)
	}

	// A producer-mode pass over an unsanitized event is the normal case.
	prod := MeshSanitize(fakePayload{}, ModeProducer)
	fresh := &mesh.Event{Kind: "message.sent", Payload: json.RawMessage(`{"a":"x"}`)}
	if out, _ := prod.Apply(t.Context(), fresh); out.Note != NoteSanitized {
		t.Fatalf("producer pass: out=%+v", out)
	}
}

// A payload that merely looks like a fail-closed one must not get past the
// hub backstop unscrubbed.
func TestBackstopDoesNotTrustASpoofedFailClosedPayload(t *testing.T) {
	hub := MeshSanitize(fakePayload{}, ModeVerifyAndScrub)
	for name, payload := range map[string]string{
		"free text":    `{"sanitize_error":"token=SECRET"}`,
		"too long":     `{"sanitize_error":"` + strings.Repeat("a", maxErrorClass+1) + `"}`,
		"upper case":   `{"sanitize_error":"Oversized"}`,
		"not a string": `{"sanitize_error":{"x":"SECRET"}}`,
		"empty class":  `{"sanitize_error":""}`,
		"extra key":    `{"sanitize_error":"oversized","leak":"SECRET"}`,
		"number":       `{"sanitize_error":1}`,
	} {
		e := &mesh.Event{Kind: "message.sent", Payload: json.RawMessage(payload)}
		out, err := hub.Apply(t.Context(), e)
		if err != nil || out.Note != NoteBackstopUnsanitized || !e.Class.Sanitized || strings.Contains(string(e.Payload), "SECRET") {
			t.Errorf("%s: out=%+v err=%v payload=%s class=%+v", name, out, err, e.Payload, e.Class)
		}
	}
	// The genuine article is still left alone.
	genuine := &mesh.Event{Kind: "message.sent", Payload: ErrorPayload("oversized")}
	if out, _ := hub.Apply(t.Context(), genuine); out.Note != NoteBackstopFailedClosed || genuine.Class != nil && genuine.Class.Sanitized {
		t.Fatalf("genuine fail-closed payload: out=%+v class=%+v", out, genuine.Class)
	}
}
