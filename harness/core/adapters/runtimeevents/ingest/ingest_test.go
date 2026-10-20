package ingest_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents/ingest"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	eventSchemaID    = "urn:hollis-labs:runtimeevents:event/v1"
	requestSchemaID  = "urn:hollis-labs:runtimeevents:ingest-request/v1"
	responseSchemaID = "urn:hollis-labs:runtimeevents:ingest-response/v1"
)

func compile(t *testing.T, id string) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	for _, r := range []struct {
		id  string
		doc []byte
	}{
		{eventSchemaID, runtimeevents.JSONSchema},
		{requestSchemaID, ingest.RequestSchema},
		{responseSchemaID, ingest.ResponseSchema},
	} {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(r.doc))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.AddResource(r.id, doc); err != nil {
			t.Fatal(err)
		}
	}
	s, err := c.Compile(id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func fixtures(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", dir, "*.json"))
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

func validate(t *testing.T, s *jsonschema.Schema, b []byte) error {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return s.Validate(inst)
}

func TestRequestFixtures(t *testing.T) {
	s := compile(t, requestSchemaID)
	for name, b := range fixtures(t, "request/valid") {
		t.Run("valid/"+name, func(t *testing.T) {
			if err := validate(t, s, b); err != nil {
				t.Fatalf("schema rejected: %v", err)
			}
			items, err := ingest.ParseRequest(b)
			if err != nil || len(items) == 0 {
				t.Fatalf("ParseRequest = %d items, %v", len(items), err)
			}
			for _, it := range items {
				if _, rej := ingest.CheckItem("cerberus", it, ingest.Options{AllowUnknownKinds: true}); rej != nil {
					t.Fatalf("CheckItem rejected a valid fixture: %v", rej)
				}
			}
		})
	}
	for name, b := range fixtures(t, "request/invalid") {
		t.Run("invalid/"+name, func(t *testing.T) {
			if err := validate(t, s, b); err == nil {
				t.Fatal("schema accepted an invalid request")
			}
			if name == "single_missing_fields.json" {
				return // ParseRequest only splits; the item check rejects it
			}
			if _, err := ingest.ParseRequest(b); err == nil {
				t.Fatal("ParseRequest accepted an invalid request")
			}
		})
	}
}

func TestResponseFixtures(t *testing.T) {
	s := compile(t, responseSchemaID)
	for name, b := range fixtures(t, "response/valid") {
		t.Run("valid/"+name, func(t *testing.T) {
			if err := validate(t, s, b); err != nil {
				t.Fatalf("schema rejected: %v", err)
			}
			var r ingest.Response
			if err := json.Unmarshal(b, &r); err != nil {
				t.Fatal(err)
			}
			if got := ingest.NewResponse(r.Results); got.Accepted != r.Accepted || got.Duplicate != r.Duplicate || got.Rejected != r.Rejected {
				t.Fatalf("counts %+v do not match results %+v", r, got)
			}
		})
	}
	for name, b := range fixtures(t, "response/invalid") {
		t.Run("invalid/"+name, func(t *testing.T) {
			if err := validate(t, s, b); err == nil {
				t.Fatal("schema accepted an invalid response")
			}
		})
	}
}

func TestParseRequestLimits(t *testing.T) {
	ev := `{"id":"evt_x"}`
	over := `{"events":[` + strings.Repeat(ev+",", ingest.MaxBatchEvents) + ev + `]}`
	if _, err := ingest.ParseRequest([]byte(over)); !errors.Is(err, ingest.ErrBatchTooLarge) {
		t.Errorf("101 events: %v", err)
	}
	atLimit := `{"events":[` + strings.Repeat(ev+",", ingest.MaxBatchEvents-1) + ev + `]}`
	if items, err := ingest.ParseRequest([]byte(atLimit)); err != nil || len(items) != ingest.MaxBatchEvents {
		t.Errorf("100 events: %d, %v", len(items), err)
	}
	big := `{"id":"` + strings.Repeat("a", ingest.MaxRequestBytes) + `"}`
	if _, err := ingest.ParseRequest([]byte(big)); !errors.Is(err, ingest.ErrRequestTooLarge) {
		t.Errorf("oversize body: %v", err)
	}
	for _, body := range []string{"", "   ", "[]", "[{}]", "null", "not json", `{"events":`} {
		if _, err := ingest.ParseRequest([]byte(body)); !errors.Is(err, ingest.ErrMalformed) {
			t.Errorf("%q: %v", body, err)
		}
	}
	if _, err := ingest.ParseRequest([]byte(`{"events":[]}`)); !errors.Is(err, ingest.ErrEmpty) {
		t.Errorf("empty batch: %v", err)
	}
}

func event(kind, app string, mut func(map[string]any)) json.RawMessage {
	m := map[string]any{
		"schema_version": "1", "id": "evt_00000000000000000000000000000b01", "kind": kind,
		"time": "2026-10-20T01:00:00Z", "app": app, "sequence": 1,
		"source":  map[string]any{"channel": "test", "confidence": "exact"},
		"subject": "msg://thing/" + app + "/1", "payload": map[string]any{"ok": true},
	}
	if mut != nil {
		mut(m)
	}
	b, _ := json.Marshal(m)
	return b
}

func TestCheckItem(t *testing.T) {
	cases := []struct {
		name string
		app  string
		raw  json.RawMessage
		opt  ingest.Options
		want ingest.Code // empty = accepted
	}{
		{"accepted", "torque", event("task.state_changed", "torque", nil), ingest.Options{}, ""},
		{"not json", "torque", json.RawMessage(`{"id":`), ingest.Options{}, ingest.CodeSchemaInvalid},
		{"missing fields", "torque", json.RawMessage(`{"id":"evt_x","kind":"alert.raised"}`), ingest.Options{}, ingest.CodeSchemaInvalid},
		{"no subject or session", "torque", event("task.state_changed", "torque", func(m map[string]any) { delete(m, "subject") }), ingest.Options{}, ingest.CodeSchemaInvalid},
		{"local visibility", "torque", event("task.state_changed", "torque", func(m map[string]any) { m["class"] = map[string]any{"visibility": "local"} }), ingest.Options{}, ingest.CodeSchemaInvalid},
		{"environment visibility", "torque", event("task.state_changed", "torque", func(m map[string]any) { m["class"] = map[string]any{"visibility": "environment"} }), ingest.Options{}, ""},
		{"payload too large", "torque", event("task.state_changed", "torque", func(m map[string]any) {
			m["payload"] = map[string]any{"blob": strings.Repeat("x", ingest.MaxPayloadBytes)}
		}), ingest.Options{}, ingest.CodePayloadTooLarge},
		{"unknown kind", "torque", event("task.invented", "torque", nil), ingest.Options{}, ingest.CodeUnknownKind},
		{"unknown kind in shadow", "torque", event("task.invented", "torque", nil), ingest.Options{AllowUnknownKinds: true}, ""},
		{"forbidden kind", "cerberus", event("task.state_changed", "cerberus", nil), ingest.Options{}, ingest.CodeForbiddenKind},
		{"unregistered app", "nobody", event("task.state_changed", "nobody", nil), ingest.Options{}, ingest.CodeForbiddenKind},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, rej := ingest.CheckItem(c.app, c.raw, c.opt)
			switch {
			case c.want == "" && rej != nil:
				t.Fatalf("rejected: %v", rej)
			case c.want != "" && rej == nil:
				t.Fatalf("accepted, want %s", c.want)
			case rej != nil && rej.Code != c.want:
				t.Fatalf("code %s (%s), want %s", rej.Code, rej.Message, c.want)
			}
		})
	}
}

func TestRejectionResult(t *testing.T) {
	_, rej := ingest.CheckItem("torque", event("task.invented", "torque", nil), ingest.Options{})
	r := rej.Result("evt_1")
	if r.Status != ingest.StatusRejected || r.Code != ingest.CodeUnknownKind || r.ID != "evt_1" || r.Cursor != "" {
		t.Fatalf("%+v", r)
	}
	s := compile(t, responseSchemaID)
	b, _ := json.Marshal(ingest.NewResponse([]ingest.Result{r}))
	if err := validate(t, s, b); err != nil {
		t.Fatalf("response schema: %v", err)
	}
}
