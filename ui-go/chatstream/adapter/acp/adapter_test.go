//nolint:misspell // "cancelled" is ACP's wire value for a stopReason
package acp_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/adapter/acp"
	"github.com/hollis-labs/go-chatstream/conformance"
)

func TestFixtures(t *testing.T) {
	conformance.CheckDecoderDir(t, acp.New(), "testdata")
}

// Truncation at every frame boundary of every fixture: never success, always
// one terminal, everything open closed first.
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
		conformance.CheckTruncation(t, acp.New(), fx)
	}
}

func TestAdapterMetadata(t *testing.T) {
	a := acp.New()
	if a.Name() != "acp" || a.Framing() != chatstream.FramingJSONRPCLines {
		t.Errorf("name %q framing %v", a.Name(), a.Framing())
	}
	if err := a.Capabilities().Validate(); err != nil {
		t.Errorf("capabilities: %v", err)
	}
	c := a.Capabilities()
	if c.Text != chatstream.GranularityChunk || c.ToolArgs != chatstream.GranularityFinal || c.Usage != chatstream.UsageNone || c.Approval != chatstream.ApprovalCapInBand || c.ResumeCursor {
		t.Errorf("capabilities over-claim: %+v", c)
	}
}

// run decodes lines and closes the decoder with a clean EOF.
func run(t *testing.T, lines ...string) []chatstream.Event {
	t.Helper()
	d := acp.New().NewDecoder(conformance.DecodeOptions())
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

func upd(u string) string {
	return `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":` + u + `}}`
}

func chunk(kind, msg, text string) string {
	m := ""
	if msg != "" {
		m = `"messageId":"` + msg + `",`
	}
	return upd(`{"sessionUpdate":"` + kind + `",` + m + `"content":{"type":"text","text":"` + text + `"}}`)
}

func verbs(evs []chatstream.Event) []string {
	var out []string
	for _, e := range evs {
		out = append(out, string(e.Verb))
	}
	return out
}

func last(evs []chatstream.Event) chatstream.Event { return evs[len(evs)-1] }

func TestStopReasonTable(t *testing.T) {
	tests := []struct {
		stop   string
		verb   chatstream.Verb
		reason chatstream.FinishReason
	}{
		{"end_turn", chatstream.VerbRunFinish, chatstream.FinishStop},
		{"max_tokens", chatstream.VerbRunFinish, chatstream.FinishLength},
		{"max_turn_requests", chatstream.VerbRunFinish, chatstream.FinishTurnLimit},
		{"refusal", chatstream.VerbRunFinish, chatstream.FinishRefusal},
		{"cancelled", chatstream.VerbRunAbort, ""},
		{"something_new", chatstream.VerbRunFinish, chatstream.FinishOther},
	}
	for _, tc := range tests {
		evs := run(t, chunk("agent_message_chunk", "", "x"), `{"jsonrpc":"2.0","id":2,"result":{"stopReason":"`+tc.stop+`"}}`)
		term := last(evs)
		if term.Verb != tc.verb {
			t.Errorf("%s: terminal %s, want %s", tc.stop, term.Verb, tc.verb)
			continue
		}
		if tc.verb == chatstream.VerbRunFinish {
			if term.Finish() != tc.reason || term.RawReason != tc.stop {
				t.Errorf("%s: reason %q raw %q, want %q", tc.stop, term.Reason, term.RawReason, tc.reason)
			}
		} else if term.Reason != "cancelled" {
			t.Errorf("%s: abort reason %q", tc.stop, term.Reason)
		}
		conformance.Check(t, evs)
	}
}

func TestErrorResponseCodes(t *testing.T) {
	evs := run(t, `{"jsonrpc":"2.0","id":2,"error":{"code":-32603,"message":"boom"}}`)
	if e := last(evs); e.Verb != chatstream.VerbRunError || e.Code != "-32603" || e.Message != "boom" || e.Retryable {
		t.Errorf("error = %+v", e)
	}
	// -32800 is "request cancelled": the client's own cancel, an abort
	evs = run(t, `{"jsonrpc":"2.0","id":2,"error":{"code":-32800,"message":"Request cancelled"}}`)
	if e := last(evs); e.Verb != chatstream.VerbRunAbort || e.Reason != "cancelled" {
		t.Errorf("cancelled error = %+v", e)
	}
	// an error to any request ends the turn: there is nothing to decode after a failed setup
	evs = run(t, `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"auth required"}}`)
	if e := last(evs); e.Verb != chatstream.VerbRunError || e.Code != "-32000" {
		t.Errorf("auth error = %+v", e)
	}
	conformance.Check(t, evs)
}

// A JSON-RPC error with loosely typed fields is still the terminal frame: it must
// not be demoted to a raw event and read as a retryable truncation.
func TestErrorResponseWithLooselyTypedFields(t *testing.T) {
	tests := []struct {
		name, line string
		verb       chatstream.Verb
		code, msg  string
	}{
		{"string code", `{"jsonrpc":"2.0","id":2,"error":{"code":"internal","message":"boom"}}`, chatstream.VerbRunError, "internal", "boom"},
		{"float code", `{"jsonrpc":"2.0","id":2,"error":{"code":-32603.0,"message":"boom"}}`, chatstream.VerbRunError, "-32603", "boom"},
		{"structured message", `{"jsonrpc":"2.0","id":2,"error":{"code":-32603,"message":{"detail":"boom"}}}`, chatstream.VerbRunError, "-32603", `{"detail":"boom"}`},
		{"error as a string", `{"jsonrpc":"2.0","id":2,"error":"boom"}`, chatstream.VerbRunError, chatstream.CodeUpstreamError, "boom"},
		{"cancelled code as a string", `{"jsonrpc":"2.0","id":2,"error":{"code":"-32800","message":"x"}}`, chatstream.VerbRunAbort, "", ""},
	}
	for _, tc := range tests {
		evs := run(t, tc.line)
		e := last(evs)
		if e.Verb != tc.verb || (tc.verb == chatstream.VerbRunError && (e.Code != tc.code || e.Message != tc.msg || e.Retryable)) {
			t.Errorf("%s: terminal = %+v", tc.name, e)
		}
		conformance.Check(t, evs)
	}
}

func TestPermissionRequestBecomesInBandApproval(t *testing.T) {
	evs := run(t,
		`{"jsonrpc":"2.0","id":"perm-9","method":"session/request_permission","params":{"sessionId":"s1","toolCall":{"toolCallId":"tc1","title":"Delete file"},"options":[{"optionId":"o1","name":"Allow","kind":"allow_once"}]}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"stopReason":"end_turn"}}`)
	var ap chatstream.Event
	for _, e := range evs {
		if e.Verb == chatstream.VerbApprovalRequest {
			ap = e
		}
	}
	if ap.Verb == "" {
		t.Fatalf("no approval.request in %v", verbs(evs))
	}
	if ap.ApprovalID != "perm-9" || ap.CallID != "tc1" || ap.Reason != "Delete file" || ap.Mode != chatstream.ApprovalInBand {
		t.Errorf("approval = %+v", ap)
	}
	if !strings.Contains(string(ap.Descriptor), `"optionId":"o1"`) {
		t.Errorf("the offered options must be in the descriptor: %s", ap.Descriptor)
	}
	// numeric ids become text
	evs = run(t, `{"jsonrpc":"2.0","id":41,"method":"session/request_permission","params":{"toolCall":{"toolCallId":"c"}}}`)
	if evs[1].ApprovalID != "41" {
		t.Errorf("approval id = %q", evs[1].ApprovalID)
	}
}

func TestOtherAgentRequestsAreRaw(t *testing.T) {
	evs := run(t, `{"jsonrpc":"2.0","id":3,"method":"fs/read_text_file","params":{"path":"/x"}}`)
	if evs[1].Verb != chatstream.VerbRaw || evs[1].Raw.Type != "fs/read_text_file" {
		t.Errorf("event = %+v", evs[1])
	}
	for _, e := range evs {
		if e.Verb == chatstream.VerbApprovalRequest {
			t.Error("only session/request_permission is an approval")
		}
	}
}

// One text part per contiguous run of chunks; anything else in between ends it.
func TestChunkContiguity(t *testing.T) {
	evs := run(t,
		chunk("agent_message_chunk", "", "a"), chunk("agent_message_chunk", "", "b"),
		upd(`{"sessionUpdate":"plan","entries":[]}`),
		chunk("agent_message_chunk", "", "c"),
		chunk("agent_thought_chunk", "", "d"),
		chunk("agent_message_chunk", "", "e"),
		`{"jsonrpc":"2.0","id":2,"result":{"stopReason":"end_turn"}}`)
	m, err := chatstream.Reduce(evs, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range m.Parts {
		got = append(got, string(p.Kind)+":"+p.Text)
	}
	want := "text:ab text:c reasoning:d text:e"
	if strings.Join(got, " ") != want {
		t.Errorf("parts = %v, want %s", got, want)
	}
	conformance.Check(t, evs)
}

func TestChunksInterleavedWithRawDoNotBreakARun(t *testing.T) {
	evs := run(t,
		chunk("agent_message_chunk", "", "a"),
		`{"jsonrpc":"2.0","id":3,"method":"fs/read_text_file","params":{}}`,
		upd(`{"sessionUpdate":"agent_message_chunk","content":{"type":"image","data":"x","mimeType":"image/png"}}`),
		chunk("agent_message_chunk", "", "b"))
	m, _ := chatstream.Reduce(evs, nil)
	if len(m.Parts) != 1 || m.Parts[0].Text != "ab" {
		t.Errorf("parts = %+v (raw events and non-text content must not split a text run)", m.Parts)
	}
}

func TestMessageIDChangeStartsANewMessage(t *testing.T) {
	evs := run(t, chunk("agent_message_chunk", "m1", "a"), chunk("agent_message_chunk", "m1", "b"), chunk("agent_message_chunk", "m2", "c"))
	var starts []string
	for _, e := range evs {
		if e.Verb == chatstream.VerbMessageStart {
			starts = append(starts, e.MessageID)
		}
	}
	if strings.Join(starts, ",") != "m1,m2" {
		t.Errorf("messages = %v", starts)
	}
	m, _ := chatstream.Reduce(evs, nil)
	if len(m.Parts) != 2 || m.Parts[0].Text != "ab" || m.Parts[1].Text != "c" {
		t.Errorf("parts = %+v", m.Parts)
	}
}

func TestSessionIDGoesInRunStartExt(t *testing.T) {
	evs := run(t, chunk("agent_message_chunk", "", "a"))
	var ext map[string]string
	if err := json.Unmarshal(evs[0].Ext["acp"], &ext); err != nil || ext["session_id"] != "s1" {
		t.Errorf("run.start ext = %s", evs[0].Ext["acp"])
	}
}

func TestToolCallShapes(t *testing.T) {
	// arguments in the call itself, result later
	evs := run(t,
		upd(`{"sessionUpdate":"tool_call","toolCallId":"t1","title":"T","kind":"execute","rawInput":{"cmd":"ls"}}`),
		upd(`{"sessionUpdate":"tool_call_update","toolCallId":"t1","status":"completed","rawOutput":"ok"}`),
		`{"jsonrpc":"2.0","id":2,"result":{"stopReason":"end_turn"}}`)
	m, err := chatstream.Reduce(evs, nil)
	if err != nil {
		t.Fatal(err)
	}
	call, res := m.Parts[0], m.Parts[1]
	if call.Kind != chatstream.PartToolCall || string(call.Arguments()) != `{"cmd":"ls"}` {
		t.Errorf("call = %+v", call)
	}
	if res.Kind != chatstream.PartToolResult || res.ID != "t1#result" || !strings.Contains(string(res.Final), `"raw_output":"ok"`) {
		t.Errorf("result = %+v", res)
	}
	if string(res.Meta["is_error"]) != "false" || string(res.Meta["call_id"]) != `"t1"` {
		t.Errorf("result meta = %v", res.Meta)
	}
	conformance.Check(t, evs)

	// failed is an error; a second terminal update is not a second result
	evs = run(t,
		upd(`{"sessionUpdate":"tool_call","toolCallId":"t2","title":"T"}`),
		upd(`{"sessionUpdate":"tool_call_update","toolCallId":"t2","status":"failed"}`),
		upd(`{"sessionUpdate":"tool_call_update","toolCallId":"t2","status":"failed"}`))
	results := 0
	for _, e := range evs {
		if e.Verb == chatstream.VerbPartStart && e.Kind == "tool_result" {
			results++
			if string(e.Meta["is_error"]) != "true" {
				t.Errorf("failed tool call must be is_error: %v", e.Meta)
			}
		}
	}
	if results != 1 {
		t.Errorf("%d results, want 1", results)
	}

	// an update for a call that was never announced opens it
	evs = run(t, upd(`{"sessionUpdate":"tool_call_update","toolCallId":"ghost","status":"completed"}`))
	conformance.Check(t, evs)
	if evs[2].Verb != chatstream.VerbMessageStart && evs[1].Verb != chatstream.VerbMessageStart {
		t.Errorf("verbs = %v", verbs(evs))
	}
}

// Parts do not interleave: a tool call still waiting for arguments is closed when
// anything that is not an update about it arrives.
func TestOpenToolCallDoesNotInterleaveWithText(t *testing.T) {
	evs := run(t,
		upd(`{"sessionUpdate":"tool_call","toolCallId":"t1","title":"T"}`),
		chunk("agent_message_chunk", "", "hello"))
	conformance.Check(t, evs)
	open := 0
	for _, e := range evs {
		if e.Verb == chatstream.VerbPartStart {
			open++
			if open > 1 {
				t.Fatalf("two parts open at once: %v", verbs(evs))
			}
		}
		if e.Verb == chatstream.VerbPartEnd {
			open--
		}
	}
}

func TestUsageUpdateIsActivityNeverUsage(t *testing.T) {
	evs := run(t, upd(`{"sessionUpdate":"usage_update","used":10,"size":100}`))
	for _, e := range evs {
		if e.Verb == chatstream.VerbUsage {
			t.Fatal("usage_update is context occupancy, not token usage of the turn")
		}
	}
	if evs[1].Verb != chatstream.VerbActivity || evs[1].Kind != "acp.usage" {
		t.Errorf("event = %+v", evs[1])
	}
}

func TestEndTurnUsageDraftIsInclusive(t *testing.T) {
	evs := run(t, `{"jsonrpc":"2.0","id":2,"result":{"stopReason":"end_turn","usage":{"totalTokens":1300,"inputTokens":1000,"outputTokens":300,"thoughtTokens":100,"cachedReadTokens":600}}}`)
	u := last(evs).Usage
	if u == nil {
		t.Fatal("no usage")
	}
	// prompt 1000 of which 600 cached; completion 300 of which 100 reasoning: no double count
	if u.UncachedInput != 400 || u.CacheRead != 600 || u.Output != 200 || u.Reasoning != 100 || u.Total() != 1300 || u.Scope != chatstream.UsageFinal {
		t.Errorf("usage = %+v", u)
	}
	// contradictory counts are dropped rather than reported wrong
	evs = run(t, `{"jsonrpc":"2.0","id":2,"result":{"stopReason":"end_turn","usage":{"inputTokens":10,"outputTokens":5,"cachedReadTokens":50}}}`)
	if last(evs).Usage != nil {
		t.Errorf("inconsistent usage must not be reported: %+v", last(evs).Usage)
	}
	conformance.Check(t, evs)
}

func TestMalformedAndUnclassifiedLinesAreRawNotTerminal(t *testing.T) {
	for _, line := range []string{`not json`, `{"jsonrpc":"2.0"}`, `42`, `{"foo":1}`} {
		evs := run(t, line, chunk("agent_message_chunk", "", "still going"), `{"jsonrpc":"2.0","id":2,"result":{"stopReason":"end_turn"}}`)
		if evs[1].Verb != chatstream.VerbRaw {
			t.Errorf("%q: event 1 = %s", line, evs[1].Verb)
		}
		if last(evs).Verb != chatstream.VerbRunFinish {
			t.Errorf("%q: %v", line, verbs(evs))
		}
		conformance.Check(t, evs)
	}
}

func TestNothingAfterTheTerminalEvent(t *testing.T) {
	d := acp.New().NewDecoder(conformance.DecodeOptions())
	if _, err := d.Decode(chatstream.Frame{Data: []byte(`{"jsonrpc":"2.0","id":2,"result":{"stopReason":"end_turn"}}`)}); err != nil {
		t.Fatal(err)
	}
	late, err := d.Decode(chatstream.Frame{Data: []byte(chunk("agent_message_chunk", "", "late"))})
	if err != nil || len(late) != 0 {
		t.Fatalf("late = %v, %v", late, err)
	}
}

func TestEOFWithoutStopReasonIsTruncation(t *testing.T) {
	evs := run(t, chunk("agent_message_chunk", "", "half a sen"))
	term := last(evs)
	if term.Verb != chatstream.VerbRunError || term.Code != chatstream.CodeUpstreamTruncated {
		t.Fatalf("terminal = %+v", term)
	}
	conformance.Check(t, evs)
}

// Every tool_call part in every fixture has a name, every result names the
// tool_call part it answers, and an approval names the part it gates.
func TestToolMetaConvention(t *testing.T) {
	paths, _ := filepath.Glob("testdata/*.frames.json")
	if len(paths) == 0 {
		t.Fatal("no fixtures")
	}
	for _, p := range paths {
		fx, err := conformance.LoadFixture(p)
		if err != nil {
			t.Fatal(err)
		}
		evs, err := conformance.DecodeFixture(acp.New(), fx)
		if err != nil {
			t.Fatal(err)
		}
		calls := map[string]bool{}
		for _, e := range evs {
			if e.Verb == chatstream.VerbPartStart && e.Kind == "tool_call" {
				calls[e.PartID] = true
				if e.MetaString(chatstream.MetaName) == "" {
					t.Errorf("%s: tool_call %s has no name", fx.Name, e.PartID)
				}
			}
			if e.Verb == chatstream.VerbPartStart && e.Kind == "tool_result" && !calls[e.MetaString(chatstream.MetaCallID)] {
				t.Errorf("%s: result %s call_id %q is not a tool_call part", fx.Name, e.PartID, e.MetaString(chatstream.MetaCallID))
			}
			if e.Verb == chatstream.VerbApprovalRequest && !calls[e.CallID] {
				t.Errorf("%s: approval call id %q is not a tool_call part", fx.Name, e.CallID)
			}
		}
	}
}

func TestToolNameAndDetail(t *testing.T) {
	tests := []struct{ update, name, detail string }{
		{`{"sessionUpdate":"tool_call","toolCallId":"a","name":"bash","title":"Run ls","kind":"execute","rawInput":{"command":"ls -la"}}`, "bash", "ls -la"},
		{`{"sessionUpdate":"tool_call","toolCallId":"a","title":"Read main.py","kind":"read","rawInput":{"path":"main.py"}}`, "Read main.py", "main.py"},
		{`{"sessionUpdate":"tool_call","toolCallId":"a","kind":"fetch","rawInput":{"url":"http://x"}}`, "fetch", "http://x"},
		{`{"sessionUpdate":"tool_call","toolCallId":"a"}`, "tool", ""},
	}
	for _, tc := range tests {
		evs := run(t, upd(tc.update))
		var st chatstream.Event
		for _, e := range evs {
			if e.Verb == chatstream.VerbPartStart && e.Kind == "tool_call" {
				st = e
			}
		}
		if st.MetaString(chatstream.MetaName) != tc.name || st.MetaString(chatstream.MetaDetail) != tc.detail {
			t.Errorf("%s: name %q detail %q, want %q %q", tc.update, st.MetaString(chatstream.MetaName), st.MetaString(chatstream.MetaDetail), tc.name, tc.detail)
		}
	}
	// a failed result is is_error and names its call
	evs := run(t, upd(`{"sessionUpdate":"tool_call","toolCallId":"a","title":"T"}`), upd(`{"sessionUpdate":"tool_call_update","toolCallId":"a","status":"failed"}`))
	for _, e := range evs {
		if e.Verb == chatstream.VerbPartStart && e.Kind == "tool_result" && (!e.MetaBool(chatstream.MetaIsError) || e.MetaString(chatstream.MetaCallID) != "a") {
			t.Errorf("result meta = %v", e.Meta)
		}
	}
	conformance.Check(t, evs)
}
