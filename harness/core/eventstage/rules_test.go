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
	got, matched := rs.Evaluate("task.state_changed", Fields{Severity: "debug", Labels: []string{"keep"}}, []byte(`{"to":"blocked"}`))
	if got.Category != "state" || got.Severity != "warning" || strings.Join(got.Labels, ",") != "blocked,keep" || len(matched) != 1 {
		t.Fatalf("got %+v matched %v", got, matched)
	}
	if _, m := rs.Evaluate("task.state_changed", Fields{}, []byte(`not json`)); len(m) != 0 {
		t.Fatalf("a payload that is not JSON must match no payload condition: %v", m)
	}
	big := []byte(`{"to":"blocked","pad":"` + strings.Repeat("x", MaxScanBytes) + `"}`)
	if _, m := rs.Evaluate("task.state_changed", Fields{}, big); len(m) != 0 {
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
