package codexappserver_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	chatstream "github.com/hollis-labs/libs/ui/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui/ui-go/chatstream/adapter/codexappserver"
	"github.com/hollis-labs/libs/ui/ui-go/chatstream/conformance"
)

func TestFixtures(t *testing.T) {
	conformance.CheckDecoderDir(t, codexappserver.New(), "testdata")
}

func fixtures(t *testing.T) map[string][]chatstream.Event {
	t.Helper()
	paths, _ := filepath.Glob("testdata/*.frames.json")
	if len(paths) == 0 {
		t.Fatal("no fixtures")
	}
	out := map[string][]chatstream.Event{}
	for _, p := range paths {
		fx, err := conformance.LoadFixture(p)
		if err != nil {
			t.Fatal(err)
		}
		evs, err := conformance.DecodeFixture(codexappserver.New(), fx)
		if err != nil {
			t.Fatal(err)
		}
		out[fx.Name] = evs
	}
	return out
}

func TestTruncationAtEveryFrameBoundary(t *testing.T) {
	paths, _ := filepath.Glob("testdata/*.frames.json")
	if len(paths) == 0 {
		t.Fatal("no fixtures")
	}
	for _, p := range paths {
		fx, err := conformance.LoadFixture(p)
		if err != nil {
			t.Fatal(err)
		}
		conformance.CheckTruncation(t, codexappserver.New(), fx)
	}
}

// hasFinal is the chat wire's rule (agent/chat HasFinal): text, reasoning and
// tool_result parts end with an object holding a string "text" (a tool_result
// also a bool "is_error"); a tool_call ends with its full input as JSON.
func hasFinal(k chatstream.PartKind, final json.RawMessage) bool {
	switch k { //nolint:exhaustive // other kinds have no rule
	case chatstream.PartToolCall:
		return len(final) > 0 && json.Valid(final)
	case chatstream.PartText, chatstream.PartReasoning:
		var obj struct {
			Text *string `json:"text"`
		}
		return len(final) > 0 && json.Unmarshal(final, &obj) == nil && obj.Text != nil
	case chatstream.PartToolResult:
		var obj struct {
			Text    *string `json:"text"`
			IsError *bool   `json:"is_error"`
		}
		return len(final) > 0 && json.Unmarshal(final, &obj) == nil && obj.Text != nil && obj.IsError != nil
	}
	return true
}

// Every part.end carries the part's complete content, and a streamed text or
// reasoning part's final text is at least what its deltas spelled.
func TestEveryPartEndCarriesFinal(t *testing.T) {
	for name, evs := range fixtures(t) {
		kinds, texts, metaErr := map[string]chatstream.PartKind{}, map[string]string{}, map[string]bool{}
		for _, e := range evs {
			switch e.Verb { //nolint:exhaustive // part events only
			case chatstream.VerbPartStart:
				kinds[e.PartID], metaErr[e.PartID] = e.PartKind(), e.MetaBool(chatstream.MetaIsError)
			case chatstream.VerbPartDelta:
				texts[e.PartID] += e.Text
			case chatstream.VerbPartEnd:
				k := kinds[e.PartID]
				if !hasFinal(k, e.Final) {
					t.Errorf("%s: part.end %q (%s) final = %s", name, e.PartID, k, e.Final)
					continue
				}
				var f struct {
					Text    string `json:"text"`
					IsError bool   `json:"is_error"`
				}
				_ = json.Unmarshal(e.Final, &f)
				if (k == chatstream.PartText || k == chatstream.PartReasoning) && name != "malformed-and-unknown" && f.Text != texts[e.PartID] {
					t.Errorf("%s: part %q final text %q, deltas spelled %q", name, e.PartID, f.Text, texts[e.PartID])
				}
				if k == chatstream.PartToolResult && f.IsError != metaErr[e.PartID] {
					t.Errorf("%s: part %q final is_error %v, meta %v", name, e.PartID, f.IsError, metaErr[e.PartID])
				}
			}
		}
	}
}

func TestExactlyOneTerminalPerFixture(t *testing.T) {
	want := map[string]chatstream.Verb{
		"text-deltas": chatstream.VerbRunFinish, "reasoning": chatstream.VerbRunFinish, "reasoning-sections": chatstream.VerbRunFinish,
		"commands-and-blocked-mcp": chatstream.VerbRunFinish, "mcp-tool-call": chatstream.VerbRunFinish, "file-change": chatstream.VerbRunFinish,
		"approval-request": chatstream.VerbRunFinish, "malformed-and-unknown": chatstream.VerbRunFinish,
		"interrupted": chatstream.VerbRunAbort, "turn-failed": chatstream.VerbRunError, "truncated": chatstream.VerbRunError,
	}
	got := fixtures(t)
	for name, evs := range got {
		var terms []chatstream.Event
		for _, e := range evs {
			if e.IsTerminal() {
				terms = append(terms, e)
			}
		}
		if len(terms) != 1 || evs[len(evs)-1].Verb != terms[0].Verb {
			t.Errorf("%s: %d terminal events", name, len(terms))
			continue
		}
		if w, ok := want[name]; !ok || terms[0].Verb != w {
			t.Errorf("%s: terminal %s, want %s", name, terms[0].Verb, w)
		}
	}
	if e := last(got["truncated"]); e.Code != chatstream.CodeUpstreamTruncated || !e.Retryable {
		t.Errorf("truncated: terminal = %+v", e)
	}
	if e := last(got["interrupted"]); e.Reason != "interrupted" {
		t.Errorf("interrupted: terminal = %+v", e)
	}
	if e := last(got["turn-failed"]); e.Code != "turn_failed" || !strings.Contains(e.Message, "no-such-model-xyz") || strings.HasPrefix(e.Message, "{") {
		t.Errorf("turn-failed: terminal = %+v", e)
	}
}

func TestAdapterMetadata(t *testing.T) {
	a := codexappserver.New()
	if a.Name() != "codex.app-server" || a.Framing() != chatstream.FramingJSONRPCLines {
		t.Errorf("name %q framing %v", a.Name(), a.Framing())
	}
	if err := a.Capabilities().Validate(); err != nil {
		t.Errorf("capabilities: %v", err)
	}
	c := a.Capabilities()
	if c.Text != chatstream.GranularityToken || c.ToolArgs != chatstream.GranularityFinal || c.Approval != chatstream.ApprovalNone ||
		!c.Cancel || c.ResumeCursor || c.Reasoning != chatstream.ReasoningSummary || c.ReasoningRoundTrip || !c.CacheTokens || !c.ReasoningTokens {
		t.Errorf("capabilities over- or under-claim: %+v", c)
	}
}

func run(t *testing.T, lines ...string) []chatstream.Event {
	t.Helper()
	d := codexappserver.New().NewDecoder(conformance.DecodeOptions())
	var out []chatstream.Event
	for _, l := range lines {
		evs, err := d.Decode(chatstream.Frame{Data: []byte(l)})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, evs...)
	}
	return append(out, d.Close(nil)...)
}

func last(evs []chatstream.Event) chatstream.Event { return evs[len(evs)-1] }

const started = `{"method":"turn/started","params":{"threadId":"th","turn":{"id":"t","status":"inProgress"}}}`

func TestTurnCompletedStatuses(t *testing.T) {
	tests := []struct {
		name, line string
		verb       chatstream.Verb
		code, msg  string
	}{
		{"completed", `{"method":"turn/completed","params":{"threadId":"th","turn":{"id":"t","status":"completed"}}}`, chatstream.VerbRunFinish, "", ""},
		{"interrupted", `{"method":"turn/completed","params":{"threadId":"th","turn":{"id":"t","status":"interrupted"}}}`, chatstream.VerbRunAbort, "", ""},
		{"failed", `{"method":"turn/completed","params":{"threadId":"th","turn":{"id":"t","status":"failed","error":{"message":"boom","codexErrorInfo":"serverOverloaded"}}}}`, chatstream.VerbRunError, "turn_failed", "boom"},
		{"failed with a wrapped upstream body", `{"method":"turn/completed","params":{"threadId":"th","turn":{"id":"t","status":"failed","error":{"message":"{\"error\":{\"message\":\"inner\"}}"}}}}`, chatstream.VerbRunError, "turn_failed", "inner"},
		{"failed with error as a string", `{"method":"turn/completed","params":{"threadId":"th","turn":{"id":"t","status":"failed","error":"boom"}}}`, chatstream.VerbRunError, "turn_failed", "boom"},
		{"unknown status", `{"method":"turn/completed","params":{"threadId":"th","turn":{"id":"t","status":"weird"}}}`, chatstream.VerbRunError, chatstream.CodeUpstreamError, `turn/completed with status "weird"`},
		{"params that do not parse", `{"method":"turn/completed","params":7}`, chatstream.VerbRunError, chatstream.CodeUpstreamError, `turn/completed with status ""`},
		{"a mistyped member", `{"method":"turn/completed","params":{"threadId":5,"turn":{"id":"t","status":"completed","error":7}}}`, chatstream.VerbRunFinish, "", ""},
	}
	for _, tc := range tests {
		evs := run(t, started, tc.line, `{"method":"item/agentMessage/delta","params":{"threadId":"th","itemId":"late","delta":"x"}}`)
		term := last(evs)
		if term.Verb != tc.verb || term.Code != tc.code || term.Message != tc.msg {
			t.Errorf("%s: terminal = %+v", tc.name, term)
		}
		n := 0
		for _, e := range evs {
			if e.IsTerminal() {
				n++
			}
			if e.PartID == "late" {
				t.Errorf("%s: a line after the terminal event was decoded", tc.name)
			}
		}
		if n != 1 {
			t.Errorf("%s: %d terminal events", tc.name, n)
		}
		conformance.Check(t, evs)
	}
}

// An error notification is not the end of the turn: codex follows a fatal one
// with turn/completed failed, and a retrying one with more of the turn.
func TestErrorNotificationIsNotTerminal(t *testing.T) {
	evs := run(t, started,
		`{"method":"error","params":{"threadId":"th","turnId":"t","willRetry":true,"error":{"message":"stream disconnected","codexErrorInfo":{"responseStreamDisconnected":{"httpStatusCode":null}}}}}`,
		`{"method":"item/completed","params":{"threadId":"th","turnId":"t","item":{"type":"agentMessage","id":"m","text":"carried on"}}}`,
		`{"method":"turn/completed","params":{"threadId":"th","turn":{"id":"t","status":"completed"}}}`)
	var act chatstream.Event
	for _, e := range evs {
		if e.Verb == chatstream.VerbActivity {
			act = e
		}
	}
	if act.Kind != "codex.error" || !strings.Contains(string(act.Value), `"will_retry":true`) {
		t.Errorf("activity = %+v", act)
	}
	m, err := chatstream.Reduce(evs, nil)
	if err != nil || m.Text() != "carried on" || m.Status != chatstream.StatusFinished {
		t.Errorf("message = %+v, %v", m, err)
	}
}

func TestApprovalRequestIsActivityOnly(t *testing.T) {
	evs := run(t, started,
		`{"id":0,"method":"item/fileChange/requestApproval","params":{"threadId":"th","turnId":"t","itemId":"f1","startedAtMs":1,"reason":"write outside root"}}`,
		`{"id":1,"method":"item/tool/requestUserInput","params":{"threadId":"th","turnId":"t","itemId":"q1","questions":[],"isBlocking":true}}`,
		`{"method":"serverRequest/resolved","params":{"threadId":"th","requestId":0}}`)
	var kinds []string
	for _, e := range evs {
		if e.Verb == chatstream.VerbApprovalRequest {
			t.Errorf("approval.request emitted; approvals are not bridged")
		}
		if e.Verb == chatstream.VerbActivity {
			kinds = append(kinds, e.Kind)
			if e.Kind == "codex.approval_request" {
				var v struct {
					RequestID int    `json:"request_id"`
					Method    string `json:"method"`
					ItemID    string `json:"item_id"`
				}
				if json.Unmarshal(e.Value, &v) != nil || v.Method != "item/fileChange/requestApproval" || v.ItemID != "f1" {
					t.Errorf("approval value = %s", e.Value)
				}
			}
		}
	}
	if strings.Join(kinds, ",") != "codex.approval_request,codex.user_input_request,codex.server_request_resolved" {
		t.Errorf("activity kinds = %v", kinds)
	}
}

// The turn's usage is the thread total at the end minus the total before the
// turn's first model call, as disjoint components.
func TestUsageIsTheTurnsShare(t *testing.T) {
	evs := fixtures(t)["reasoning-sections"]
	u := last(evs).Usage
	if u == nil {
		t.Fatal("no usage")
	}
	// before: input 900 (cached 360), output 90 (reasoning 18); after: input 1120 (cached 480, write 10), output 130 (reasoning 25)
	// disjoint: uncached 630-540, cache read 480-360, cache write 10, output 105-72, reasoning 25-18
	want := chatstream.Usage{Scope: chatstream.UsageFinal, UncachedInput: 90, CacheRead: 120, CacheWrite: 10, Output: 33, Reasoning: 7}
	if u.Scope != want.Scope || u.UncachedInput != want.UncachedInput || u.CacheRead != want.CacheRead ||
		u.CacheWrite != want.CacheWrite || u.Output != want.Output || u.Reasoning != want.Reasoning {
		t.Errorf("usage = %+v, want %+v", *u, want)
	}
}
