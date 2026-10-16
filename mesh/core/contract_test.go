package mesh_test

import (
	"encoding/json"
	mesh "github.com/hollis-labs/libs/mesh/core"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNegotiationRequiresExactVersionVerbAndMode(t *testing.T) {
	d := mesh.Descriptor{Provider: "msg://service/test/provider", DefaultLimits: mesh.Limits{MaxDepth: 1, MaxChildren: 1, FanOut: 1, Budget: 1, Timeout: time.Minute}, Capabilities: []mesh.Capability{{URI: "urn:test:mesh/v1", Verbs: []mesh.Verb{mesh.Delegate}, Modes: []string{"async"}}}}
	for _, req := range []mesh.Requirement{{URI: "urn:test:mesh/v2"}, {URI: "urn:test:mesh/v1", Verbs: []mesh.Verb{mesh.AgentLaunch}}, {URI: "urn:test:mesh/v1", Modes: []string{"sync"}}} {
		if _, err := mesh.NegotiateCapabilities(d, []mesh.Requirement{req}); err == nil {
			t.Fatalf("accepted unsupported requirement %+v", req)
		}
	}
	if _, err := mesh.NegotiateCapabilities(d, []mesh.Requirement{{URI: "urn:test:mesh/v1", Verbs: []mesh.Verb{mesh.Delegate}, Modes: []string{"async"}}}); err != nil {
		t.Fatal(err)
	}
}
func TestEventRoundTripPreservesOrderingAndAttribution(t *testing.T) {
	e := mesh.Event{SchemaVersion: "1", ID: "e1", Kind: "message.send", Time: time.Now().UTC(), Actor: mesh.Actor{URN: "msg://agent/test/worker", Kind: mesh.ActorAgent}, OnBehalfOf: []mesh.URN{"msg://user/test/owner"}, Subject: "msg://channel/test/run", Generation: 2, SourceSequence: 1, Cursor: "durable-99", CausationID: "cause", CorrelationID: "work", InReplyTo: "message", ContentType: "application/json", Visibility: "private", Truncated: true, Payload: json.RawMessage(`{"text":"hello"}`)}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var got mesh.Event
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Cursor != e.Cursor || got.SourceSequence != 1 || got.Generation != 2 || got.Actor != e.Actor || got.OnBehalfOf[0] != e.OnBehalfOf[0] || got.CausationID != e.CausationID || !got.Truncated {
		t.Fatal("envelope lost ordering or provenance")
	}
}
func TestEventV11BlocksRoundTripAndStayOptional(t *testing.T) {
	off := int64(42)
	at := time.Date(2026, 10, 16, 5, 0, 0, 0, time.UTC)
	e := mesh.Event{SchemaVersion: "1", ID: "evt_1", Kind: "task.state_changed", Time: at, Actor: mesh.Actor{URN: "msg://service/torque/scheduler", Kind: mesh.ActorService}, Subject: "msg://task/torque/CW-1", Generation: 1, SourceSequence: 3, Cursor: "env:9", Visibility: "environment", Payload: json.RawMessage(`{}`),
		RawOffset: &off, RecordedAt: &at, Depth: 2, ActorRole: "reaction", ActorVerified: "token",
		Scope: &mesh.EventScope{ProjectID: "p1", TaskID: "CW-1", Attempt: 2},
		Class: &mesh.EventClass{Category: "state", Severity: "info", Sensitivity: "private", PII: true, Labels: []string{"a"}, Sanitized: true, Redactions: 1}}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var got mesh.Event
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, e) {
		t.Fatalf("v1.1 blocks lost in round trip:\n got %+v\nwant %+v", got, e)
	}

	// An event without the new fields marshals with none of their keys, so
	// v1 readers and stored v1 bytes see no change.
	e.RawOffset, e.RecordedAt, e.Depth, e.ActorRole, e.ActorVerified, e.Scope, e.Class = nil, nil, 0, "", "", nil, nil
	b, err = json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"raw_offset", "recorded_at", "depth", "actor_role", "actor_verified", "scope", "class"} {
		if strings.Contains(string(b), `"`+key+`"`) {
			t.Fatalf("unset v1.1 field %q was marshalled: %s", key, b)
		}
	}
}
func TestVocabularyRejectsInvalidActorsAndNonFiniteLimits(t *testing.T) {
	for _, a := range []mesh.Actor{{URN: "msg://user/test/owner", Kind: mesh.ActorAgent}, {URN: "https://example.com", Kind: mesh.ActorService}, {URN: "msg://agent", Kind: mesh.ActorAgent}} {
		if err := a.Validate(); err == nil {
			t.Fatalf("accepted invalid actor %+v", a)
		}
	}
	limits := mesh.Limits{MaxDepth: 1, MaxChildren: 1, FanOut: 1, Budget: math.NaN(), Timeout: time.Second}
	if limits.Validate() == nil {
		t.Fatal("accepted NaN budget")
	}
	if mesh.HistoryPolicy("").Effective() != mesh.HistorySummary {
		t.Fatal("incorrect default history")
	}
	if !mesh.TaskCompleted.Terminal() || mesh.TaskPaused.Terminal() {
		t.Fatal("incorrect terminal state classification")
	}
}

func TestToolActorAttribution(t *testing.T) {
	tool := mesh.Actor{URN: "msg://tool/test/search", Kind: mesh.ActorTool}
	if err := tool.Validate(); err != nil {
		t.Fatal(err)
	}
	tool.URN = "msg://service/test/search"
	if tool.Validate() == nil {
		t.Fatal("tool accepted another actor kind's address")
	}
}
