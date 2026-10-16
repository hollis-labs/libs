package eventstage

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	mesh "github.com/hollis-labs/libs/mesh/core"
)

func loadDefault(t *testing.T) *RuleSet {
	t.Helper()
	data, err := os.ReadFile("testdata/default.rules.json")
	if err != nil {
		t.Fatal(err)
	}
	rs, err := ParseRules(data)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func TestDefaultRulesPassTheirOwnTests(t *testing.T) {
	rs := loadDefault(t)
	if len(rs.Tests) == 0 {
		t.Fatal("the default rule set must carry tests")
	}
	for _, err := range rs.SelfTest(t.Context()) {
		t.Error(err)
	}
}

func TestParseRulesRejectsBadDocuments(t *testing.T) {
	ok := func(mutate string) string {
		return strings.Replace(`{"schema":"eventstage.rules/v1","id":"x","version":"1","rules":[RULES]}`, "RULES", mutate, 1)
	}
	cases := map[string]string{
		"wrong schema":           `{"schema":"other","id":"x","version":"1","rules":[]}`,
		"missing version":        `{"schema":"eventstage.rules/v1","id":"x","rules":[]}`,
		"unknown field":          ok(`{"id":"a","match":{},"set":{"labels":["l"]},"extra":1}`),
		"cannot set sensitivity": ok(`{"id":"a","match":{},"set":{"labels":["l"],"sensitivity":"public"}}`),
		"cannot set sanitized":   ok(`{"id":"a","match":{},"set":{"labels":["l"],"sanitized":true}}`),
		"duplicate id":           ok(`{"id":"a","match":{},"set":{"labels":["l"]}},{"id":"a","match":{},"set":{"labels":["m"]}}`),
		"empty set":              ok(`{"id":"a","match":{},"set":{}}`),
		"bad regexp":             ok(`{"id":"a","match":{"payload":[{"path":"/x","matches":"("}]},"set":{"labels":["l"]}}`),
		"two operators":          ok(`{"id":"a","match":{"payload":[{"path":"/x","equals":"1","exists":true}]},"set":{"labels":["l"]}}`),
		"no operator":            ok(`{"id":"a","match":{"payload":[{"path":"/x"}]},"set":{"labels":["l"]}}`),
		"path not a pointer":     ok(`{"id":"a","match":{"payload":[{"path":"x","exists":true}]},"set":{"labels":["l"]}}`),
		"unknown severity":       ok(`{"id":"a","match":{},"set":{"severity":"loud"}}`),
		"unknown category":       ok(`{"id":"a","match":{},"set":{"category":"misc"}}`),
		"bad kind pattern":       ok(`{"id":"a","match":{"kind":["task.*.x*"]},"set":{"labels":["l"]}}`),
		"label with space":       ok(`{"id":"a","match":{},"set":{"labels":["a b"]}}`),
		"trailing data":          ok(`{"id":"a","match":{},"set":{"labels":["l"]}}`) + ` {}`,
	}
	for name, doc := range cases {
		if _, err := ParseRules([]byte(doc)); err == nil || !strings.Contains(err.Error(), "invalid rule set") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestEvaluateRules(t *testing.T) {
	rs := loadDefault(t)
	got, matched := rs.Evaluate(EventView{Kind: "task.state_changed", Fields: Fields{Severity: "debug", Labels: []string{"keep"}}, Sanitized: true, Payload: []byte(`{"to":"blocked"}`)})
	if got.Category != "state" || got.Severity != "warning" || strings.Join(got.Labels, ",") != "blocked,keep" || len(matched) != 1 {
		t.Fatalf("got %+v matched %v", got, matched)
	}
	if _, m := rs.Evaluate(EventView{Kind: "task.state_changed", Sanitized: true, Payload: []byte(`not json`)}); len(m) != 0 {
		t.Fatalf("a payload that is not JSON must match no payload condition: %v", m)
	}
	big := []byte(`{"to":"blocked","pad":"` + strings.Repeat("x", MaxScanBytes) + `"}`)
	if _, m := rs.Evaluate(EventView{Kind: "task.state_changed", Sanitized: true, Payload: big}); len(m) != 0 {
		t.Fatalf("payloads over MaxScanBytes are not inspected: %v", m)
	}
}

// A rule that keys on a secret must not fire while the event is unsanitized.
func TestClassifyIgnoresThePayloadOfAnUnsanitizedEvent(t *testing.T) {
	rs, err := ParseRules([]byte(`{"schema":"eventstage.rules/v1","id":"x","version":"1","rules":[
	  {"id":"leak","match":{"payload":[{"path":"/token","matches":"^sk-"}]},"set":{"labels":["leaked"]}},
	  {"id":"kind-only","match":{"kind":["message.sent"]},"set":{"labels":["msg"]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	e := rtEvent(`{"token":"sk-live-123"}`)
	st := RuntimeEventsClassify(rs)
	if _, err := st.Apply(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(e.Class.Labels, ","); got != "msg" {
		t.Fatalf("unsanitized event labels = %q, want only kind-based rules", got)
	}

	// The same payload after Sanitize redacted it: the rule cannot match.
	c, _ := NewChain(Config[runtimeevents.Event]{
		Sanitize: RuntimeEventsSanitize(redactTokens{}, ModeProducer),
		Classify: st,
	})
	e = rtEvent(`{"token":"sk-live-123"}`)
	c.Apply(t.Context(), e)
	if strings.Contains(string(e.Payload), "sk-live") || strings.Join(e.Class.Labels, ",") != "msg" {
		t.Fatalf("payload=%s labels=%v", e.Payload, e.Class.Labels)
	}
}

type redactTokens struct{}

func (redactTokens) SanitizePayload(_ context.Context, _ string, p []byte) ([]byte, PayloadReport, error) {
	var m map[string]any
	if err := json.Unmarshal(p, &m); err != nil {
		return nil, PayloadReport{ErrorClass: "invalid_json"}, nil
	}
	n := uint32(0)
	for k, v := range m {
		if s, ok := v.(string); ok && strings.HasPrefix(s, "sk-") {
			m[k] = "[redacted]"
			n++
		}
	}
	out, _ := json.Marshal(m)
	return out, PayloadReport{Redactions: n, Sensitivity: "secret"}, nil
}

func TestMeshClassifyWritesTheClassBlock(t *testing.T) {
	rs := loadDefault(t)
	e := &mesh.Event{Kind: "tool.call.finished", Payload: json.RawMessage(`{"ok":false}`), Class: &mesh.EventClass{Sanitized: true}}
	if _, err := MeshClassify(rs).Apply(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if e.Class.Category != "tool" || e.Class.Severity != "error" || !e.Class.Sanitized {
		t.Fatalf("class = %+v", e.Class)
	}
}

// A fail-closed event has sanitized=false, so its payload is withheld; the
// sanitized match field is how a rule still labels it. This runs the real
// chain, not a hand-built event.
func TestFailClosedEventIsLabelledByASanitizedFalseRule(t *testing.T) {
	rs := loadDefault(t)
	c, _ := NewChain(Config[runtimeevents.Event]{
		Sanitize: RuntimeEventsSanitize(fakePayload{}, ModeProducer),
		Classify: RuntimeEventsClassify(rs),
	})
	e := rtEvent(`{"x":"TOOBIG SECRET"}`)
	rep := c.Apply(t.Context(), e)
	if !rep.Emit() || e.Class.Sanitized {
		t.Fatalf("emit=%v class=%+v", rep.Emit(), e.Class)
	}
	if got := strings.Join(e.Class.Labels, ","); got != "sanitize-failed" || e.Class.Severity != "warning" {
		t.Fatalf("labels=%q severity=%q, want the sanitize-failed rule to fire", got, e.Class.Severity)
	}
	// The same rule must not fire for an event that sanitized normally.
	ok := rtEvent(`{"x":"fine"}`)
	c.Apply(t.Context(), ok)
	if strings.Contains(strings.Join(ok.Class.Labels, ","), "sanitize-failed") {
		t.Fatalf("sanitized event labelled sanitize-failed: %v", ok.Class.Labels)
	}
}

func TestCatalogFillsAnEmptyCategoryAndRulesCannotOverrideIt(t *testing.T) {
	rs := loadDefault(t)
	e := &mesh.Event{Kind: "task.state_changed", Payload: json.RawMessage(`{"to":"blocked"}`), Class: &mesh.EventClass{Sanitized: true}}
	if _, err := MeshClassify(rs).Apply(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if e.Class.Category != "state" {
		t.Fatalf("category = %q, want the catalog's", e.Class.Category)
	}
	// With no rule matching at all the category is still filled.
	plain := &mesh.Event{Kind: "message.sent", Payload: json.RawMessage(`{}`), Class: &mesh.EventClass{Sanitized: true}}
	if out, err := MeshClassify(rs).Apply(t.Context(), plain); err != nil || !out.Changed || plain.Class.Category != "message" {
		t.Fatalf("out=%+v err=%v class=%+v", out, err, plain.Class)
	}
}

// A retention tombstone carries payload_digest and no payload. The Sanitize
// adapters leave it exactly as it is: they never call the sanitizer, never set
// class.sanitized (a tombstone is not "sanitized" and not "unsanitized"), and
// Classify, which sees a payload only from a sanitized event, labels it on kind
// alone.
func TestTombstonedEventsAreLeftAloneBySanitizeAndClassifiedOnKindOnly(t *testing.T) {
	rs := loadDefault(t)
	for _, mode := range []Mode{ModeProducer, ModeVerifyAndScrub} {
		for _, file := range []string{"testdata/tombstoned.mesh.json", "testdata/tombstoned.unsanitized.mesh.json"} {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var e mesh.Event
			if err := json.Unmarshal(data, &e); err != nil {
				t.Fatal(err)
			}
			if !e.Tombstoned() {
				t.Fatalf("%s: the fixture is not a tombstone", file)
			}
			digest, wasSanitized := e.PayloadDigest, e.Class != nil && e.Class.Sanitized
			c, _ := NewChain(Config[mesh.Event]{Sanitize: MeshSanitize(panicSanitizer{}, mode), Classify: MeshClassify(rs)})
			rep := c.Apply(t.Context(), &e)
			if !rep.Emit() || rep.Err() != nil || rep.Steps[0].Outcome.Note != NoteTombstoned || rep.Steps[0].Outcome.Changed {
				t.Fatalf("%s mode %d: report = %+v err=%v", file, mode, rep, rep.Err())
			}
			if string(e.Payload) != "null" || e.PayloadDigest != digest || !e.Tombstoned() {
				t.Fatalf("%s: payload=%s digest=%q", file, e.Payload, e.PayloadDigest)
			}
			if got := e.Class != nil && e.Class.Sanitized; got != wasSanitized {
				t.Fatalf("%s mode %d: class.sanitized went from %v to %v", file, mode, wasSanitized, got)
			}
			if e.Class == nil || e.Class.Category != "state" || strings.Contains(strings.Join(e.Class.Labels, ","), "blocked") {
				t.Fatalf("%s: class = %+v: classified on kind only, no payload rule may fire", file, e.Class)
			}
		}
	}
}

func TestTombstonedRuntimeEventIsLeftAlone(t *testing.T) {
	data, err := os.ReadFile("testdata/tombstoned.runtimeevents.json")
	if err != nil {
		t.Fatal(err)
	}
	var e runtimeevents.Event
	if err := json.Unmarshal(data, &e); err != nil {
		t.Fatal(err)
	}
	if !e.Tombstoned() || e.PayloadDigest != "withheld" {
		t.Fatalf("the fixture is not a tombstone: %+v", e)
	}
	c, _ := NewChain(Config[runtimeevents.Event]{Sanitize: RuntimeEventsSanitize(panicSanitizer{}, ModeVerifyAndScrub), Classify: RuntimeEventsClassify(loadDefault(t))})
	rep := c.Apply(t.Context(), &e)
	if !rep.Emit() || rep.Err() != nil || rep.Steps[0].Outcome.Note != NoteTombstoned {
		t.Fatalf("report = %+v err=%v", rep, rep.Err())
	}
	if e.Class.Sanitized || e.PayloadDigest != "withheld" || len(e.Payload) != 0 || e.Class.Category != "state" {
		t.Fatalf("event = %+v class=%+v", e, e.Class)
	}
}
