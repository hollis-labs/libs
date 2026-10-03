package chatstream

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func ev(verb Verb, f func(*Event)) Event {
	e := Event{V: SchemaVersion, RunID: "r1", Time: t0, Verb: verb}
	if f != nil {
		f(&e)
	}
	return e
}

// sample is a complete run: a reasoning part, a text part, a tool call with
// streamed arguments, cumulative then final usage, and a finish.
func sample() []Event {
	seq := uint64(0)
	add := func(e Event) Event { seq++; e.Seq = seq; return e }
	return []Event{
		add(ev(VerbRunStart, func(e *Event) { e.Provider, e.Model = "anthropic", "claude" })),
		add(ev(VerbStepStart, func(e *Event) { e.StepID = "s1" })),
		add(ev(VerbMessageStart, func(e *Event) { e.MessageID, e.Role = "m1", "assistant" })),
		add(ev(VerbUsage, func(e *Event) { e.Usage = &Usage{Scope: UsageCumulative, UncachedInput: 10, CacheRead: 90} })),
		add(ev(VerbPartStart, func(e *Event) { e.PartID, e.Kind = "p1", string(PartReasoning) })),
		add(ev(VerbPartDelta, func(e *Event) { e.PartID, e.Text = "p1", "hmm " })),
		add(ev(VerbPartDelta, func(e *Event) { e.PartID, e.Text = "p1", "ok" })),
		add(ev(VerbPartEnd, func(e *Event) { e.PartID, e.Final = "p1", json.RawMessage(`{"signature":"abc"}`) })),
		add(ev(VerbPartStart, func(e *Event) { e.PartID, e.Kind = "p2", string(PartText) })),
		add(ev(VerbPartDelta, func(e *Event) { e.PartID, e.Text = "p2", "Hello" })),
		add(ev(VerbPartDelta, func(e *Event) { e.PartID, e.Text = "p2", ", world" })),
		add(ev(VerbPartEnd, func(e *Event) { e.PartID = "p2" })),
		add(ev(VerbPartStart, func(e *Event) { e.PartID, e.Kind = "p3", string(PartToolCall) })),
		add(ev(VerbPartDelta, func(e *Event) { e.PartID, e.JSONFragment = "p3", `{"city":` })),
		add(ev(VerbPartDelta, func(e *Event) { e.PartID, e.JSONFragment = "p3", `"Oslo"}` })),
		add(ev(VerbPartEnd, func(e *Event) { e.PartID = "p3" })),
		add(ev(VerbRaw, func(e *Event) { e.Raw = &Raw{Dialect: "anthropic", Type: "ping"} })),
		add(ev(VerbUsage, func(e *Event) { e.Usage = &Usage{Scope: UsageCumulative, UncachedInput: 10, CacheRead: 90, Output: 42} })),
		add(ev(VerbMessageEnd, nil)),
		add(ev(VerbStepFinish, func(e *Event) { e.StepID = "s1" })),
		add(ev(VerbRunFinish, func(e *Event) {
			e.Reason, e.RawReason = string(FinishToolCalls), "tool_use"
			e.Usage = &Usage{Scope: UsageFinal, UncachedInput: 10, CacheRead: 90, Output: 42}
		})),
	}
}

func TestReduceBuildsTheMessage(t *testing.T) {
	m, err := Reduce(sample(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != StatusFinished || m.FinishReason != FinishToolCalls || m.RawReason != "tool_use" {
		t.Errorf("status %s reason %s/%s", m.Status, m.FinishReason, m.RawReason)
	}
	if m.RunID != "r1" || m.ID != "m1" || m.Role != "assistant" || m.Provider != "anthropic" || m.Model != "claude" {
		t.Errorf("header = %+v", m)
	}
	if m.Text() != "Hello, world" {
		t.Errorf("Text = %q", m.Text())
	}
	if len(m.Parts) != 3 || m.Parts[0].Kind != PartReasoning || m.Parts[0].Text != "hmm ok" || string(m.Parts[0].Final) != `{"signature":"abc"}` {
		t.Errorf("reasoning part = %+v", m.Parts[0])
	}
	if got := m.Parts[2].Arguments(); string(got) != `{"city":"Oslo"}` {
		t.Errorf("tool arguments = %s", got)
	}
	for _, p := range m.Parts {
		if p.Open {
			t.Errorf("part %s left open", p.ID)
		}
	}
	if m.Usage == nil || m.Usage.Scope != UsageFinal || m.Usage.Total() != 142 {
		t.Errorf("usage = %+v", m.Usage)
	}
	if len(m.Raws) != 1 || len(m.Steps) != 1 || m.Steps[0].Open {
		t.Errorf("raws %d steps %+v", len(m.Raws), m.Steps)
	}
	if m.LastSeq != uint64(len(sample())) {
		t.Errorf("LastSeq = %d", m.LastSeq)
	}
}

func canonicalJSON(t *testing.T, m *Message) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Replay from any cursor, from the snapshot at that cursor (in memory or
// stored and reloaded), equals a full replay.
func TestReduceReplayFromAnyCursorEqualsFullReplay(t *testing.T) {
	events := sample()
	full, err := Reduce(events, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := canonicalJSON(t, full)
	for k := 0; k <= len(events); k++ {
		head, err := Reduce(events[:k], nil)
		if err != nil {
			t.Fatalf("k=%d: %v", k, err)
		}
		stored, _ := json.Marshal(head)
		var restored Message
		if err := json.Unmarshal(stored, &restored); err != nil {
			t.Fatal(err)
		}
		for name, snap := range map[string]*Message{"memory": head, "stored": &restored} {
			rest, err := Reduce(events[k:], snap)
			if err != nil {
				t.Fatalf("k=%d %s: %v", k, name, err)
			}
			if got := canonicalJSON(t, rest); got != want {
				t.Fatalf("k=%d %s snapshot:\n got %s\nwant %s", k, name, got, want)
			}
		}
	}
}

func TestReduceSkipsEventsAlreadyApplied(t *testing.T) {
	events := sample()
	half, err := Reduce(events[:10], nil)
	if err != nil {
		t.Fatal(err)
	}
	// an overlapping replay: events 5.. again, on top of the snapshot at 10
	got, err := Reduce(events[5:], half)
	if err != nil {
		t.Fatal(err)
	}
	full, _ := Reduce(events, nil)
	if canonicalJSON(t, got) != canonicalJSON(t, full) {
		t.Fatal("an overlapping replay must not apply events twice")
	}
}

func TestReduceDoesNotModifyItsInput(t *testing.T) {
	events := sample()
	head, _ := Reduce(events[:8], nil)
	before := canonicalJSON(t, head)
	if _, err := Reduce(events[8:], head); err != nil {
		t.Fatal(err)
	}
	if canonicalJSON(t, head) != before {
		t.Fatal("Reduce modified the prior message")
	}
}

func TestReduceDeltaUsageAccumulates(t *testing.T) {
	events := []Event{
		ev(VerbRunStart, nil),
		ev(VerbUsage, func(e *Event) { e.Usage = &Usage{Scope: UsageDelta, UncachedInput: 5, Output: 1} }),
		ev(VerbUsage, func(e *Event) { e.Usage = &Usage{Scope: UsageDelta, Output: 4, Reasoning: 2} }),
		ev(VerbRunFinish, func(e *Event) { e.Reason = string(FinishStop) }),
	}
	m, err := Reduce(events, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Usage.UncachedInput != 5 || m.Usage.Output != 5 || m.Usage.Reasoning != 2 {
		t.Errorf("usage = %+v", m.Usage)
	}
}

func TestReduceGapMarksTheMessageIncomplete(t *testing.T) {
	m, err := Reduce([]Event{
		ev(VerbRunStart, nil),
		ev(VerbGap, func(e *Event) { e.From, e.To, e.Reason = 5, 9, GapRetention }),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Incomplete || len(m.Gaps) != 1 || m.Gaps[0].Reason != GapRetention {
		t.Errorf("message = %+v", m)
	}
}

func TestReduceRejectsBrokenLifecycles(t *testing.T) {
	part := func(id string, k PartKind) Event {
		return ev(VerbPartStart, func(e *Event) { e.PartID, e.Kind = id, string(k) })
	}
	tests := []struct {
		name   string
		events []Event
		want   string
	}{
		{"delta before start", []Event{ev(VerbPartDelta, func(e *Event) { e.PartID, e.Text = "p", "x" })}, "not open"},
		{"delta after end", []Event{part("p", PartText), ev(VerbPartEnd, func(e *Event) { e.PartID = "p" }), ev(VerbPartDelta, func(e *Event) { e.PartID, e.Text = "p", "x" })}, "not open"},
		{"end unknown part", []Event{ev(VerbPartEnd, func(e *Event) { e.PartID = "nope" })}, "not open"},
		{"id reuse while open", []Event{part("p", PartText), part("p", PartText)}, "already open"},
		{"text on a tool call", []Event{part("p", PartToolCall), ev(VerbPartDelta, func(e *Event) { e.PartID, e.Text = "p", "x" })}, "tool_call"},
		{"json on text", []Event{part("p", PartText), ev(VerbPartDelta, func(e *Event) { e.PartID, e.JSONFragment = "p", "{}" })}, "text part"},
		{"both text and json", []Event{part("p", PartToolCall), ev(VerbPartDelta, func(e *Event) { e.PartID, e.Text, e.JSONFragment = "p", "a", "b" })}, "both"},
		{"two run.starts", []Event{ev(VerbRunStart, nil), ev(VerbRunStart, nil)}, "twice"},
		{"message.end without message", []Event{ev(VerbMessageEnd, nil)}, "no open message"},
		{"message inside message", []Event{ev(VerbMessageStart, func(e *Event) { e.MessageID = "a" }), ev(VerbMessageStart, func(e *Event) { e.MessageID = "b" })}, "inside message"},
		{"step.finish unopened", []Event{ev(VerbStepFinish, func(e *Event) { e.StepID = "s" })}, "not open"},
		{"usage without usage", []Event{ev(VerbUsage, nil)}, "without usage"},
		{"another run's event", []Event{ev(VerbRunStart, nil), {V: SchemaVersion, RunID: "other", Time: t0, Verb: VerbRaw}}, "run id"},
	}
	for _, tc := range tests {
		_, err := Reduce(tc.events, nil)
		if !errors.Is(err, ErrInvalidEvent) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want ErrInvalidEvent mentioning %q", tc.name, err, tc.want)
		}
	}
}

func TestReduceRejectsAnythingAfterTheTerminalEvent(t *testing.T) {
	for _, term := range []Verb{VerbRunFinish, VerbRunError, VerbRunAbort} {
		_, err := Reduce([]Event{ev(VerbRunStart, nil), ev(term, nil), ev(VerbRaw, nil)}, nil)
		if !errors.Is(err, ErrAfterTerminal) {
			t.Errorf("after %s: err = %v", term, err)
		}
	}
}

func TestReduceTerminalStatuses(t *testing.T) {
	cases := map[Verb]RunStatus{VerbRunFinish: StatusFinished, VerbRunError: StatusErrored, VerbRunAbort: StatusAborted}
	for verb, want := range cases {
		m, err := Reduce([]Event{ev(VerbRunStart, nil), ev(verb, func(e *Event) { e.Code, e.Message, e.Reason = "c", "boom", "user" })}, nil)
		if err != nil || m.Status != want || !m.Status.Done() {
			t.Errorf("%s: %+v, %v", verb, m, err)
		}
	}
	m, _ := Reduce([]Event{ev(VerbRunStart, nil), ev(VerbRunError, func(e *Event) { e.Code, e.Retryable = "upstream_truncated", true })}, nil)
	if m.Error == nil || m.Error.Code != "upstream_truncated" || !m.Error.Retryable {
		t.Errorf("error = %+v", m.Error)
	}
}

func TestReduceIgnoresVerbsItDoesNotKnow(t *testing.T) {
	m, err := Reduce([]Event{ev(VerbRunStart, nil), ev("future.verb", nil), ev(VerbRunFinish, nil)}, nil)
	if err != nil || m.Status != StatusFinished {
		t.Fatalf("%+v, %v", m, err)
	}
}

func TestPartArgumentsFallsBackToValidFragments(t *testing.T) {
	if got := (Part{Args: `{"a":1}`}).Arguments(); string(got) != `{"a":1}` {
		t.Errorf("valid fragments: %s", got)
	}
	if got := (Part{Args: `{"a":`}).Arguments(); got != nil {
		t.Errorf("incomplete fragments must not be returned as arguments: %s", got)
	}
	if got := (Part{Args: `{"a":`, Final: json.RawMessage(`{"a":2}`)}).Arguments(); string(got) != `{"a":2}` {
		t.Errorf("final wins: %s", got)
	}
}

// Every verb's payload survives a JSON round trip, and the flat struct never
// gives two Go fields one JSON key (encoding/json would silently drop both).
func TestEventJSONRoundTripForEveryVerb(t *testing.T) {
	exp := t0.Add(time.Hour)
	events := []Event{
		ev(VerbRunStart, func(e *Event) { e.ParentRunID, e.Provider, e.Model = "p", "openai", "gpt" }),
		ev(VerbRunFinish, func(e *Event) {
			e.Reason, e.RawReason, e.Usage = "stop", "end_turn", &Usage{Scope: UsageFinal, Output: 3}
		}),
		ev(VerbRunError, func(e *Event) { e.Code, e.Retryable, e.Message = "x", true, "m" }),
		ev(VerbRunAbort, func(e *Event) { e.Reason = "canceled" }),
		ev(VerbStepStart, func(e *Event) { e.StepID, e.Name = "s", "n" }),
		ev(VerbMessageStart, func(e *Event) { e.MessageID, e.Role = "m", "assistant" }),
		ev(VerbPartStart, func(e *Event) {
			e.PartID, e.Kind, e.Meta = "p", "tool_call", map[string]json.RawMessage{"name": json.RawMessage(`"get"`)}
		}),
		ev(VerbPartDelta, func(e *Event) { e.PartID, e.Text = "p", "hi" }),
		ev(VerbPartDelta, func(e *Event) { e.PartID, e.JSONFragment = "p", `{"a"` }),
		ev(VerbPartEnd, func(e *Event) { e.PartID, e.Final = "p", json.RawMessage(`{"a":1}`) }),
		ev(VerbApprovalRequest, func(e *Event) {
			e.ApprovalID, e.CallID, e.Reason, e.Descriptor, e.Mode, e.ExpiresAt = "a", "c", "why", json.RawMessage(`{"tool":"rm"}`), ApprovalInBand, &exp
		}),
		ev(VerbUsage, func(e *Event) {
			e.Usage = &Usage{Scope: UsageDelta, UncachedInput: 1, CacheRead: 2, CacheWrite: 3, Output: 4, Reasoning: 5}
		}),
		ev(VerbActivity, func(e *Event) {
			e.Kind, e.Value, e.Patch = "nanite.slot_changed", json.RawMessage(`{"a":1}`), json.RawMessage(`[1]`)
		}),
		ev(VerbRaw, func(e *Event) { e.Raw = &Raw{Dialect: "acp", Type: "plan", Payload: json.RawMessage(`{"x":1}`)} }),
		ev(VerbGap, func(e *Event) { e.From, e.To, e.Reason = 3, 9, GapDroppedSlow }),
	}
	for i := range events {
		events[i].Ext = map[string]json.RawMessage{"anthropic": json.RawMessage(`{"k":1}`)}
		raw, err := json.Marshal(events[i])
		if err != nil {
			t.Fatal(err)
		}
		var back Event
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatalf("%s: %v", events[i].Verb, err)
		}
		// Usage.Extra and time formatting are compared through JSON to ignore representation
		a, _ := json.Marshal(back)
		if string(a) != string(raw) {
			t.Errorf("%s did not round trip:\n%s\n%s", events[i].Verb, raw, a)
		}
		if !reflect.DeepEqual(back.Verb, events[i].Verb) {
			t.Errorf("verb lost: %s", back.Verb)
		}
	}
}

// encoding/json silently drops BOTH fields when two share a JSON key, so the
// flat Event struct must never have that. Reason and Kind are shared on
// purpose: one Go field each.
func TestEventHasNoDuplicateJSONKeys(t *testing.T) {
	rt := reflect.TypeOf(Event{})
	seen := map[string]string{}
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "" {
			t.Errorf("field %s has no json tag", f.Name)
			continue
		}
		if prev, dup := seen[name]; dup {
			t.Errorf("json key %q is used by both %s and %s", name, prev, f.Name)
		}
		seen[name] = f.Name
	}
	// every payload the verbs document is present as a key
	for _, key := range []string{"v", "seq", "run_id", "time", "verb", "parent_run_id", "provider", "model", "reason", "raw_reason",
		"code", "retryable", "message", "step_id", "name", "message_id", "role", "part_id", "kind", "meta", "text",
		"json_fragment", "final", "approval_id", "call_id", "descriptor", "mode", "expires_at", "usage", "value", "patch",
		"from", "to", "ext", "raw"} {
		if _, ok := seen[key]; !ok {
			t.Errorf("no field carries json key %q", key)
		}
	}
}

// A gap can swallow the event that opened a part. From then on the message is
// incomplete, and the events that belonged to what was lost are skipped rather
// than rejected; without a gap the same stream is an error.
func TestReduceAfterAGapSkipsWhatItsOpeningLost(t *testing.T) {
	orphans := []Event{
		ev(VerbPartDelta, func(e *Event) { e.PartID, e.Text = "lost", "x" }),
		ev(VerbPartEnd, func(e *Event) { e.PartID = "lost" }),
		ev(VerbMessageEnd, nil),
		ev(VerbStepFinish, func(e *Event) { e.StepID = "lost" }),
		ev(VerbRunFinish, func(e *Event) { e.Reason = string(FinishStop) }),
	}
	if _, err := Reduce(orphans, nil); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("without a gap: err = %v", err)
	}
	withGap := append([]Event{ev(VerbGap, func(e *Event) { e.From, e.To, e.Reason = 2, 6, GapRetention })}, orphans...)
	m, err := Reduce(withGap, nil)
	if err != nil {
		t.Fatalf("after a gap: %v", err)
	}
	if !m.Incomplete || m.Status != StatusFinished || len(m.Parts) != 0 {
		t.Errorf("message = %+v", m)
	}
	// a delta for a part that was never opened is still an error when the gap comes later
	if _, err := Reduce([]Event{orphans[0], ev(VerbGap, nil)}, nil); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("a gap does not excuse an earlier violation: %v", err)
	}
}
