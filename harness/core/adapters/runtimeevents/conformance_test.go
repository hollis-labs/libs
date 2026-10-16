package runtimeevents_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	mesh "github.com/hollis-labs/libs/mesh/core"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const schemaID = "urn:hollis-labs:runtimeevents:event/v1"

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(runtimeevents.JSONSchema))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	if err := c.AddResource(schemaID, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(schemaID)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func fixtures(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "conformance", dir, "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no fixtures in %s: %v", dir, err)
	}
	out := map[string][]byte{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(p)] = b
	}
	return out
}

func schemaValid(t *testing.T, s *jsonschema.Schema, b []byte) error {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return s.Validate(inst)
}

func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(x, y)
}

// Every valid fixture passes the JSON Schema and Validate, and survives
// runtimeevents -> mesh.Event -> JSON -> mesh.Event -> runtimeevents
// unchanged.
func TestConformanceValidFixturesRoundTripThroughMesh(t *testing.T) {
	s := compileSchema(t)
	for name, b := range fixtures(t, "valid") {
		t.Run(name, func(t *testing.T) {
			if err := schemaValid(t, s, b); err != nil {
				t.Fatalf("schema rejected valid fixture: %v", err)
			}
			var e runtimeevents.Event
			if err := json.Unmarshal(b, &e); err != nil {
				t.Fatal(err)
			}
			if err := e.Validate(); err != nil {
				t.Fatalf("Validate rejected valid fixture: %v", err)
			}
			m, err := runtimeevents.ToMesh(e)
			if err != nil {
				t.Fatal(err)
			}
			wire, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			var m2 mesh.Event
			if err := json.Unmarshal(wire, &m2); err != nil {
				t.Fatal(err)
			}
			back, err := runtimeevents.FromMesh(m2)
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(back)
			if err != nil {
				t.Fatal(err)
			}
			if !sameJSON(t, b, got) {
				t.Fatalf("round trip changed the event:\n in  %s\n out %s", b, got)
			}
		})
	}
}

// Every invalid fixture is rejected by both the JSON Schema and Validate.
func TestConformanceInvalidFixturesRejected(t *testing.T) {
	s := compileSchema(t)
	for name, b := range fixtures(t, "invalid") {
		t.Run(name, func(t *testing.T) {
			if err := schemaValid(t, s, b); err == nil {
				t.Fatal("schema accepted invalid fixture")
			}
			var e runtimeevents.Event
			if err := json.Unmarshal(b, &e); err != nil {
				t.Fatal(err)
			}
			if err := e.Validate(); !errors.Is(err, runtimeevents.ErrInvalid) {
				t.Fatalf("Validate = %v, want ErrInvalid", err)
			}
		})
	}
}

// Every mesh fixture survives mesh.Event -> runtimeevents -> mesh.Event
// unchanged, and the runtimeevents form passes the schema.
func TestConformanceMeshFixturesRoundTrip(t *testing.T) {
	s := compileSchema(t)
	for name, b := range fixtures(t, "mesh") {
		t.Run(name, func(t *testing.T) {
			var m mesh.Event
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatal(err)
			}
			e, err := runtimeevents.FromMesh(m)
			if err != nil {
				t.Fatal(err)
			}
			eb, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			if err := schemaValid(t, s, eb); err != nil {
				t.Fatalf("schema rejected projected event: %v\n%s", err, eb)
			}
			back, err := runtimeevents.ToMesh(e)
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(back)
			if err != nil {
				t.Fatal(err)
			}
			if !sameJSON(t, b, got) {
				t.Fatalf("round trip changed the event:\n in  %s\n out %s", b, got)
			}
		})
	}
}

// A v1 event that sets no v1.1 field marshals without any v1.1 key, so
// existing producers' output is unchanged.
func TestV1EventMarshalsWithoutV11Keys(t *testing.T) {
	e := runtimeevents.Event{SchemaVersion: runtimeevents.SchemaVersion, ID: "evt_1", Kind: runtimeevents.KindSessionIdle, Time: time.Unix(0, 0).UTC(), SessionID: "ses_1", Sequence: 1, Source: runtimeevents.Source{Channel: runtimeevents.ChannelPTY}}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"subject", "generation", "recorded_at", "cursor", "content_type", "truncated", "scope", "corr", "actor", "class", "payload_digest"} {
		if bytes.Contains(b, []byte(`"`+key+`"`)) {
			t.Fatalf("unset v1.1 field %q was marshalled: %s", key, b)
		}
	}
}

func TestSessionBound(t *testing.T) {
	for k, want := range map[runtimeevents.EventKind]bool{
		runtimeevents.KindSessionIdle:        true,
		runtimeevents.KindTurnStarted:        true,
		"task.state_changed":                 false,
		"message.sent":                       false,
		"sessionless.thing":                  false,
		runtimeevents.KindPlantStarted:       true,
		runtimeevents.KindAgentToolUse:       true,
		runtimeevents.KindInterruptRequested: true,
	} {
		if got := runtimeevents.SessionBound(k); got != want {
			t.Errorf("SessionBound(%q) = %v, want %v", k, got, want)
		}
	}
}
