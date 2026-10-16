package eventstage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents/catalog"
	mesh "github.com/hollis-labs/libs/mesh/core"
	"github.com/hollis-labs/libs/security/redact"
)

func TestPayloadSanitizerPolicyPrecedenceAndCopies(t *testing.T) {
	policies := map[string]redact.Policy{
		"sample.*":           {AllowedPaths: []string{"/family"}},
		"sample.child.*":     {AllowedPaths: []string{"/child"}},
		"sample.child.exact": {AllowedPaths: []string{"/exact"}},
	}
	sanitizer, err := NewPayloadSanitizer(policies)
	if err != nil {
		t.Fatal(err)
	}
	policies["sample.*"].AllowedPaths[0] = "/mutated"
	delete(policies, "sample.child.exact")
	for _, tc := range []struct{ kind, want string }{
		{"sample.sibling", `{"family":1}`},
		{"sample.child.other", `{"child":2}`},
		{"sample.child.exact", `{"exact":3}`},
		{"samplex.child", `{}`},
		{"sample.", `{}`},
	} {
		out, _, err := sanitizer.SanitizePayload(t.Context(), tc.kind, []byte(`{"family":1,"child":2,"exact":3,"mutated":4}`))
		if err != nil || string(out) != tc.want {
			t.Errorf("%s: %s, %v", tc.kind, out, err)
		}
	}
}

func TestPayloadSanitizerCatalogDefaults(t *testing.T) {
	sanitizer, err := NewPayloadSanitizer(nil)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{catalog.StatusStable, catalog.StatusDeprecated, catalog.StatusNew} {
		t.Run(status, func(t *testing.T) {
			kind := ""
			for _, entry := range entries.Kinds {
				if entry.Status == status && entry.Kind != "stdout.raw" && entry.Kind != "stderr.raw" && entry.Kind != "stdin.write" {
					kind = entry.Kind
					break
				}
			}
			if kind == "" {
				t.Fatalf("need a %s kind to exercise fallback", status)
			}
			out, rep, err := sanitizer.SanitizePayload(t.Context(), kind, []byte(`{"detail":"ordinary"}`))
			want := `{"detail":"ordinary"}`
			if status == catalog.StatusNew {
				want = `{}`
			}
			if err != nil || string(out) != want || rep.ErrorClass != "" {
				t.Fatalf("%s: %s, %+v, %v", kind, out, rep, err)
			}
		})
	}
	out, rep, err := sanitizer.SanitizePayload(t.Context(), "uncatalogued.test", []byte(`{"detail":"ordinary"}`))
	if err != nil || string(out) != `{}` || rep.Redactions != 1 {
		t.Fatalf("unknown: %s, %+v, %v", out, rep, err)
	}
}

func TestPayloadSanitizerOpaqueRawKindsAndJSONStringText(t *testing.T) {
	sanitizer, err := NewPayloadSanitizer(map[string]redact.Policy{"explicit.text": {Mode: redact.Denylist}})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"stdout.raw", "stderr.raw", "stdin.write"} {
		_, rep, err := sanitizer.SanitizePayload(t.Context(), kind, []byte(`"c2FtcGxl"`))
		if err != nil || rep.ErrorClass != string(redact.ErrorOpaque) {
			t.Fatalf("%s: %+v, %v", kind, rep, err)
		}
	}
	out, rep, err := sanitizer.SanitizePayload(t.Context(), "explicit.text", []byte(`"contact person@example.org"`))
	if err != nil || !json.Valid(out) || bytes.Contains(out, []byte("person@example.org")) || !rep.PII || rep.Sensitivity != "private" {
		t.Fatalf("text: %s, %+v, %v", out, rep, err)
	}
	_, rep, err = sanitizer.SanitizePayload(t.Context(), "explicit.text", []byte("not JSON"))
	if err != nil || rep.ErrorClass != string(redact.ErrorInvalidJSON) {
		t.Fatalf("invalid JSON: %+v, %v", rep, err)
	}
	explicit, err := NewPayloadSanitizer(map[string]redact.Policy{"stdout.raw": {Mode: redact.Denylist}})
	if err != nil {
		t.Fatal(err)
	}
	out, rep, err = explicit.SanitizePayload(t.Context(), "stdout.raw", []byte(`"plain text"`))
	if err != nil || string(out) != `"plain text"` || rep.ErrorClass != "" {
		t.Fatalf("explicit raw override: %s, %+v, %v", out, rep, err)
	}
}

func TestPayloadSanitizerMapsReportAndIsConcurrent(t *testing.T) {
	policy := redact.Policy{Mode: redact.Denylist, Sensitivity: redact.Private}
	sanitizer, err := NewPayloadSanitizer(map[string]redact.Policy{"sample.event": policy})
	if err != nil {
		t.Fatal(err)
	}
	core, err := redact.NewSanitizer(policy)
	if err != nil {
		t.Fatal(err)
	}
	input := []byte(`{"password":"demo","contact":"person@example.org"}`)
	expected, want := core.Sanitize(input)
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			for range 10 {
				out, rep, err := sanitizer.SanitizePayload(context.Background(), "sample.event", input)
				if err != nil || !bytes.Equal(out, expected) || rep.Redactions != want.Redactions || !reflect.DeepEqual(rep.Paths, want.Paths) || rep.Sensitivity != string(want.Sensitivity) || rep.PII != want.PII || rep.ErrorClass != string(want.ErrorClass) {
					t.Errorf("report mapping: %+v, %v", rep, err)
					return
				}
				if len(out) > 0 {
					out[0] = 'x'
				}
				if len(rep.Paths) > 0 {
					rep.Paths[0] = "mutated"
				}
			}
		})
	}
	workers.Wait()
	if string(input) != `{"password":"demo","contact":"person@example.org"}` {
		t.Fatal("input changed")
	}
	_, rep, err := sanitizer.SanitizePayload(t.Context(), "unknown.kind", []byte(`{"person@example.org":"demo"}`))
	if err != nil || strings.Contains(strings.Join(rep.Paths, " "), "person@example.org") {
		t.Fatalf("key leaked in report: %+v, %v", rep, err)
	}
}

func TestPayloadSanitizerValidatesPoliciesAndCancellation(t *testing.T) {
	for _, policies := range []map[string]redact.Policy{
		{"": {}}, {"*": {}}, {".*": {}}, {"a.*.b": {}}, {" a.b": {}},
		{"a.b": {AllowedPaths: []string{"not-a-pointer"}}},
		{"a.b": {Mode: redact.Mode(99)}},
	} {
		if _, err := NewPayloadSanitizer(policies); err == nil {
			t.Errorf("accepted invalid policy: %+v", policies)
		}
	}
	sanitizer, err := NewPayloadSanitizer(nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := sanitizer.SanitizePayload(ctx, "sample.event", []byte(`{}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
}

func TestFailClosedClassesWriterReaderLockstep(t *testing.T) {
	classes := FailClosedClasses()
	present := map[string]bool{}
	for _, class := range classes {
		if class == "" || present[class] {
			t.Fatalf("invalid shared class %q", class)
		}
		present[class] = true
		payload := ErrorPayload(class)
		if !failClosedPayload(payload) || classOf(classedError(class)) != class {
			t.Fatalf("class %q does not roundtrip", class)
		}
		event := &mesh.Event{Kind: "sample.event", Payload: payload}
		out, err := MeshSanitize(panicSanitizer{}, ModeVerifyAndScrub).Apply(t.Context(), event)
		if err != nil || out.Note != NoteBackstopFailedClosed || out.Changed || !bytes.Equal(event.Payload, payload) {
			t.Fatalf("backstop class %q: %+v, %v", class, out, err)
		}
	}
	for _, class := range redact.ErrorClasses() {
		if !present[string(class)] {
			t.Fatalf("missing redact class %q", class)
		}
	}
	for _, class := range []string{"panic", "error", ErrorClassTombstoneWithPayload} {
		if !present[class] {
			t.Fatalf("missing stage class %q", class)
		}
	}
	classes[0] = "mutated"
	if FailClosedClasses()[0] == "mutated" || failClosedPayload([]byte(`{"sanitize_error":"mutated"}`)) {
		t.Fatal("shared list is mutable")
	}
	for _, class := range []string{"hunter2", "Bad Class/with SECRET!", "", "OVERSIZED"} {
		if string(ErrorPayload(class)) != `{"sanitize_error":"error"}` || classOf(classedError(class)) != "error" {
			t.Fatalf("unknown class leaked: %q", class)
		}
	}
}

func TestBackstopRejectsValidPatternNonmemberAndDuplicateKeys(t *testing.T) {
	sanitizer, err := NewPayloadSanitizer(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{
		`{"sanitize_error":"hunter2"}`,
		`{"sanitize_error":"hunter2","sanitize_error":"error"}`,
		`{"sanitize_error":"error","sanitize_error":"hunter2"}`,
		`{"sanitize_error":"error"} {"extra":1}`,
	} {
		if failClosedPayload([]byte(payload)) {
			t.Fatalf("trusted spoof: %s", payload)
		}
		event := &mesh.Event{Kind: "unknown.event", Payload: json.RawMessage(payload)}
		chain, err := NewChain(Config[mesh.Event]{Sanitize: MeshSanitize(sanitizer, ModeVerifyAndScrub)})
		if err != nil {
			t.Fatal(err)
		}
		report := chain.Apply(t.Context(), event)
		if !report.Emit() || bytes.Contains(event.Payload, []byte("hunter2")) {
			t.Fatalf("spoof survived: %s, %v", event.Payload, report.Err())
		}
		if payload == `{"sanitize_error":"hunter2"}` && report.Steps[0].Outcome.Note != NoteBackstopUnsanitized {
			t.Fatalf("nonmember bypassed backstop: %+v", report)
		}
	}
}

func TestPayloadSanitizerProducerAndBackstopEnvelopeMapping(t *testing.T) {
	sanitizer, err := NewPayloadSanitizer(map[string]redact.Policy{"sample.event": {Mode: redact.Denylist}})
	if err != nil {
		t.Fatal(err)
	}
	event := rtEvent(`{"contact":"person@example.org"}`)
	event.Kind = "sample.event"
	producer, err := NewChain(Config[runtimeevents.Event]{Sanitize: RuntimeEventsSanitize(sanitizer, ModeProducer)})
	if err != nil {
		t.Fatal(err)
	}
	report := producer.Apply(t.Context(), event)
	if report.Err() != nil || !event.Class.Sanitized || !event.Class.PII || event.Class.Sensitivity != "private" || event.Class.Redactions == 0 {
		t.Fatalf("producer: %+v, %v", event.Class, report.Err())
	}
	hub := &mesh.Event{Kind: string(event.Kind), Payload: append([]byte(nil), event.Payload...), Class: &mesh.EventClass{Sanitized: true, PII: event.Class.PII, Sensitivity: event.Class.Sensitivity, Redactions: event.Class.Redactions}}
	before := append([]byte(nil), hub.Payload...)
	count := hub.Class.Redactions
	outcome, err := MeshSanitize(sanitizer, ModeVerifyAndScrub).Apply(t.Context(), hub)
	if err != nil || outcome.Note != NoteSanitized || !bytes.Equal(before, hub.Payload) || hub.Class.Redactions != count || !hub.Class.PII {
		t.Fatalf("backstop: %+v, %+v, %v", outcome, hub.Class, err)
	}
	// Core failures map to a known label without propagating payload or error text.
	event.Payload = []byte(`not JSON`)
	report = producer.Apply(t.Context(), event)
	if report.Err() == nil || event.Class.Sanitized || string(event.Payload) != `{"sanitize_error":"invalid_json"}` {
		t.Fatalf("failure: %s, %v", event.Payload, report.Err())
	}
}
