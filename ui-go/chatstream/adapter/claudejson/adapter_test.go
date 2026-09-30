package claudejson_test

import (
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/adapter/claudejson"
	"github.com/hollis-labs/go-chatstream/conformance"
)

func TestCapabilitiesAreValid(t *testing.T) {
	a := claudejson.New()
	if err := a.Capabilities().Validate(); err != nil {
		t.Fatal(err)
	}
	if a.Name() != "claude.stream-json" {
		t.Errorf("Name = %q", a.Name())
	}
}

func TestFixtures(t *testing.T) { conformance.CheckDecoderDir(t, claudejson.New(), "testdata") }

func TestTruncationAtEveryFrame(t *testing.T) {
	paths, _ := filepath.Glob("testdata/*.frames.json")
	if len(paths) == 0 {
		t.Fatal("no fixtures")
	}
	for _, p := range paths {
		fx, err := conformance.LoadFixture(p)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(fx.Name, func(t *testing.T) { conformance.CheckTruncation(t, claudejson.New(), fx) })
	}
}

// ---- dialect tests ----

func decode(t *testing.T, opts chatstream.DecodeOptions, lines []string, cause error, closeIt bool) []chatstream.Event {
	t.Helper()
	if opts.Now == nil {
		opts.Now = func() time.Time { return conformance.FixedTime }
	}
	d := claudejson.New().NewDecoder(opts)
	var out []chatstream.Event
	for i, l := range lines {
		evs, err := d.Decode(chatstream.Frame{Data: []byte(l)})
		if err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		out = append(out, evs...)
	}
	if closeIt {
		out = append(out, d.Close(cause)...)
	}
	return out
}

const initLine = `{"type":"system","subtype":"init","session_id":"S1","model":"claude-opus-5"}`

func terminals(evs []chatstream.Event) []chatstream.Event {
	var out []chatstream.Event
	for _, e := range evs {
		if e.IsTerminal() {
			out = append(out, e)
		}
	}
	return out
}

func last(evs []chatstream.Event) chatstream.Event { return evs[len(evs)-1] }

func result(fields string) string {
	return `{"type":"result","usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":20,"cache_read_input_tokens":30},` + fields + `}`
}

// The result line picks the outcome; this is the whole table.
func TestResultMapping(t *testing.T) {
	type want struct {
		verb   chatstream.Verb
		reason string // finish reason, abort reason, or error code
		raw    string
	}
	table := []struct {
		name   string
		fields string
		want   want
	}{
		{"completed end_turn", `"subtype":"success","is_error":false,"terminal_reason":"completed","stop_reason":"end_turn"`, want{chatstream.VerbRunFinish, "stop", "end_turn"}},
		{"completed no stop_reason", `"subtype":"success","is_error":false,"terminal_reason":"completed"`, want{chatstream.VerbRunFinish, "stop", "completed"}},
		{"no terminal_reason", `"subtype":"success","is_error":false,"stop_reason":"max_tokens"`, want{chatstream.VerbRunFinish, "length", "max_tokens"}},
		{"nothing at all", `"subtype":"success","is_error":false`, want{chatstream.VerbRunFinish, "stop", ""}},
		{"completed tool_use", `"subtype":"success","terminal_reason":"completed","stop_reason":"tool_use"`, want{chatstream.VerbRunFinish, "tool_calls", "tool_use"}},
		{"completed refusal", `"subtype":"success","terminal_reason":"completed","stop_reason":"refusal"`, want{chatstream.VerbRunFinish, "refusal", "refusal"}},
		{"completed pause_turn", `"subtype":"success","terminal_reason":"completed","stop_reason":"pause_turn"`, want{chatstream.VerbRunFinish, "pause", "pause_turn"}},
		{"completed context exceeded", `"subtype":"success","terminal_reason":"completed","stop_reason":"model_context_window_exceeded"`, want{chatstream.VerbRunFinish, "context_exceeded", "model_context_window_exceeded"}},
		{"max_turns", `"subtype":"error_max_turns","is_error":true,"terminal_reason":"max_turns"`, want{chatstream.VerbRunFinish, "turn_limit", "max_turns"}},
		{"max_turns by subtype only", `"subtype":"error_max_turns","is_error":true`, want{chatstream.VerbRunFinish, "turn_limit", "max_turns"}},
		{"prompt_too_long", `"subtype":"error_during_execution","is_error":true,"terminal_reason":"prompt_too_long"`, want{chatstream.VerbRunFinish, "context_exceeded", "prompt_too_long"}},
		{"aborted_streaming", `"subtype":"error_during_execution","is_error":true,"terminal_reason":"aborted_streaming"`, want{chatstream.VerbRunAbort, "aborted_streaming", ""}},
		{"aborted_tools", `"subtype":"success","is_error":false,"terminal_reason":"aborted_tools"`, want{chatstream.VerbRunAbort, "aborted_tools", ""}},
		{"tool_deferred", `"subtype":"success","is_error":false,"terminal_reason":"tool_deferred"`, want{chatstream.VerbRunFinish, "other", "tool_deferred"}},
		{"hook_stopped", `"subtype":"success","terminal_reason":"hook_stopped"`, want{chatstream.VerbRunFinish, "other", "hook_stopped"}},
		{"error subtype", `"subtype":"error_during_execution","is_error":true,"terminal_reason":"model_error","errors":["a","b"]`, want{chatstream.VerbRunError, "error_during_execution", ""}},
		{"budget", `"subtype":"error_max_budget_usd","is_error":true,"terminal_reason":"budget_exhausted"`, want{chatstream.VerbRunError, "error_max_budget_usd", ""}},
		{"is_error on success subtype", `"subtype":"success","is_error":true,"terminal_reason":"api_error","result":"API Error: 500"`, want{chatstream.VerbRunError, "api_error", ""}},
	}
	for _, tc := range table {
		evs := decode(t, chatstream.DecodeOptions{}, []string{initLine, result(tc.fields)}, nil, true)
		terms := terminals(evs)
		if len(terms) != 1 || !last(evs).IsTerminal() {
			t.Errorf("%s: terminals = %+v", tc.name, terms)
			continue
		}
		got := terms[0]
		reason := got.Reason
		if got.Verb == chatstream.VerbRunError {
			reason = got.Code
		}
		if got.Verb != tc.want.verb || reason != tc.want.reason || (tc.want.raw != "" && got.RawReason != tc.want.raw) {
			t.Errorf("%s: %s reason %q raw %q, want %s %q %q", tc.name, got.Verb, reason, got.RawReason, tc.want.verb, tc.want.reason, tc.want.raw)
		}
		conformance.Check(t, evs)
	}
}

func TestResultErrorDetails(t *testing.T) {
	evs := decode(t, chatstream.DecodeOptions{}, []string{initLine,
		result(`"subtype":"error_during_execution","is_error":true,"terminal_reason":"api_error","errors":["boom","bang"],"num_turns":3`)}, nil, true)
	e := last(evs)
	if e.Message != "boom; bang" || !e.Retryable {
		t.Errorf("error = %+v", e)
	}
	// non-retryable terminal reasons
	evs = decode(t, chatstream.DecodeOptions{}, []string{initLine, result(`"subtype":"error_during_execution","is_error":true,"terminal_reason":"blocking_limit","result":"limit hit"`)}, nil, true)
	if e := last(evs); e.Retryable || e.Message != "limit hit" {
		t.Errorf("error = %+v", e)
	}
	// the usage is reported before an error or an abort, since only finish carries it inline
	evs = decode(t, chatstream.DecodeOptions{}, []string{initLine, result(`"subtype":"error_during_execution","is_error":true`)}, nil, true)
	if evs[len(evs)-2].Verb != chatstream.VerbUsage || evs[len(evs)-2].Usage.Scope != chatstream.UsageFinal {
		t.Errorf("no usage before the error: %v", evs)
	}
}

// result.usage is Anthropic-shaped (input excludes cache): the components add up
// to what was billed.
func TestResultUsageIsDisjoint(t *testing.T) {
	evs := decode(t, chatstream.DecodeOptions{}, []string{initLine, result(`"subtype":"success"`)}, nil, true)
	u := last(evs).Usage
	if u == nil || u.UncachedInput != 10 || u.CacheWrite != 20 || u.CacheRead != 30 || u.Output != 5 || u.Total() != 65 || u.Scope != chatstream.UsageFinal {
		t.Fatalf("usage = %+v", u)
	}
	// a result with no usage object has none
	evs = decode(t, chatstream.DecodeOptions{}, []string{initLine, `{"type":"result","subtype":"success"}`}, nil, true)
	if last(evs).Usage != nil {
		t.Errorf("usage = %+v", last(evs).Usage)
	}
}

// Only result ends the run: every one of these lines is an error of some kind
// and none of them is terminal.
func TestOnlyResultIsTerminal(t *testing.T) {
	lines := []string{
		initLine,
		`{"type":"system","subtype":"api_retry","attempt":1,"error":"overloaded"}`,
		`{"type":"assistant","message":{"id":"a","content":[{"type":"text","text":"x"}]},"error":"rate_limit"}`,
		`{"type":"assistant","message":{"id":"b","content":[{"type":"text","text":"y"}]},"aborted":true}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"err","is_error":true}]}}`,
		`{"type":"error","message":"something"}`,
		`{"type":"system","subtype":"permission_denied","tool_name":"Bash"}`,
		`not json`,
	}
	evs := decode(t, chatstream.DecodeOptions{}, lines, nil, false)
	if len(terminals(evs)) != 0 {
		t.Fatalf("terminal before result: %+v", terminals(evs))
	}
	// ...and the result then ends it
	evs = decode(t, chatstream.DecodeOptions{}, append(lines, result(`"subtype":"success"`)), nil, true)
	if terms := terminals(evs); len(terms) != 1 || terms[0].Verb != chatstream.VerbRunFinish {
		t.Fatalf("terminals = %+v", terms)
	}
	conformance.Check(t, evs)
}

func TestLinesAfterResultAreIgnored(t *testing.T) {
	evs := decode(t, chatstream.DecodeOptions{}, []string{initLine, result(`"subtype":"success"`),
		`{"type":"assistant","message":{"id":"late","content":[{"type":"text","text":"late"}]}}`, result(`"subtype":"success"`)}, nil, true)
	if len(terminals(evs)) != 1 || !last(evs).IsTerminal() {
		t.Fatalf("events after the result: %+v", evs)
	}
}

// With --include-partial-messages the CLI sends each block twice, streamed and
// then whole. The whole one is skipped: the text appears once.
func TestStreamedMessagesAreNotDuplicated(t *testing.T) {
	fx, err := conformance.LoadFixture("testdata/partial_stream.frames.json")
	if err != nil {
		t.Fatal(err)
	}
	evs, err := conformance.DecodeFixture(claudejson.New(), fx)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := chatstream.Reduce(evs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Text() != "Let me look.Done." {
		t.Errorf("text = %q (each block must appear once)", msg.Text())
	}
	var tools int
	for _, p := range msg.Parts {
		if p.Kind == chatstream.PartToolCall {
			tools++
		}
	}
	if tools != 1 || len(msg.Steps) != 2 {
		t.Errorf("tool calls %d, steps %d", tools, len(msg.Steps))
	}
	if msg.Status != chatstream.StatusFinished {
		t.Errorf("status = %s (message_stop must not end the run)", msg.Status)
	}
	// usage comes from the result alone: stream_event usage restarts every model call
	for _, e := range evs {
		if e.Verb == chatstream.VerbUsage {
			t.Errorf("unexpected usage event %+v", e.Usage)
		}
	}
}

func TestWholeMessagesBecomeParts(t *testing.T) {
	fx, _ := conformance.LoadFixture("testdata/whole_messages.frames.json")
	evs, _ := conformance.DecodeFixture(claudejson.New(), fx)
	msg, err := chatstream.Reduce(evs, nil)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[chatstream.PartKind]int{}
	for _, p := range msg.Parts {
		kinds[p.Kind]++
	}
	if kinds[chatstream.PartReasoning] != 1 || kinds[chatstream.PartText] != 2 || kinds[chatstream.PartToolCall] != 2 || kinds[chatstream.PartToolResult] != 2 {
		t.Errorf("kinds = %v", kinds)
	}
	if string(msg.Parts[0].Final) != `{"signature":"sig123"}` {
		t.Errorf("signature = %s", msg.Parts[0].Final)
	}
	if got := string(msg.Parts[2].Arguments()); got != `{"command":"ls"}` {
		t.Errorf("arguments = %s", got)
	}
}

func TestControlRequestBecomesAnInBandApproval(t *testing.T) {
	evs := decode(t, chatstream.DecodeOptions{}, []string{initLine,
		`{"type":"assistant","message":{"id":"a","content":[{"type":"tool_use","id":"tu","name":"Bash","input":{"command":"rm x"}}]}}`,
		`{"type":"control_request","request_id":"r9","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"tu","description":"run rm","input":{"command":"rm x"}}}`,
		`{"type":"control_request","request_id":"r10","request":{"tool_name":"Edit"}}`,
		`{"type":"control_request","request_id":"r11","request":{"subtype":"initialize"}}`,
		`{"type":"control_request","request":{"subtype":"can_use_tool"}}`,
	}, nil, false)
	var ap []chatstream.Event
	var raws int
	for _, e := range evs {
		switch e.Verb { //nolint:exhaustive // only these two matter here
		case chatstream.VerbApprovalRequest:
			ap = append(ap, e)
		case chatstream.VerbRaw:
			raws++
		}
	}
	if len(ap) != 2 || ap[0].ApprovalID != "r9" || ap[0].CallID != "a:0" || ap[0].Mode != chatstream.ApprovalInBand || ap[0].Reason != "run rm" ||
		!strings.Contains(string(ap[0].Descriptor), `"tool_name":"Bash"`) || ap[1].ApprovalID != "r10" || ap[1].Reason != "can_use_tool" {
		t.Errorf("approvals = %+v", ap)
	}
	if raws != 2 {
		t.Errorf("non-permission and id-less control requests must be raw events: %d", raws)
	}
}

func TestRunIDComesFromOptionsOrSession(t *testing.T) {
	evs := decode(t, chatstream.DecodeOptions{}, []string{initLine, result(`"subtype":"success"`)}, nil, true)
	for _, e := range evs {
		if e.RunID != "S1" {
			t.Fatalf("run id = %q, want the session id", e.RunID)
		}
	}
	evs = decode(t, chatstream.DecodeOptions{RunID: "mine", Provider: "acme"}, []string{initLine, result(`"subtype":"success"`)}, nil, true)
	if evs[0].RunID != "mine" || evs[0].Provider != "acme" || evs[0].Model != "claude-opus-5" {
		t.Errorf("run.start = %+v", evs[0])
	}
}

func TestRunStartsEvenWithoutInit(t *testing.T) {
	evs := decode(t, chatstream.DecodeOptions{}, []string{`{"type":"assistant","message":{"id":"a","content":[{"type":"text","text":"hi"}]},"session_id":"S2"}`, result(`"subtype":"success"`)}, nil, true)
	if evs[0].Verb != chatstream.VerbRunStart || evs[0].RunID != "S2" {
		t.Fatalf("first = %+v", evs[0])
	}
	conformance.Check(t, evs)
	// a second init after the run started is just activity
	evs = decode(t, chatstream.DecodeOptions{}, []string{initLine, initLine}, nil, false)
	if evs[1].Verb != chatstream.VerbActivity || evs[1].Kind != "claude.system.init" {
		t.Errorf("second init = %+v", evs[1])
	}
}

func TestSubagentMessagesCarryTheirParent(t *testing.T) {
	evs := decode(t, chatstream.DecodeOptions{}, []string{initLine,
		`{"type":"assistant","message":{"id":"sub","content":[{"type":"tool_use","id":"tu2","name":"Read","input":{"p":1}}]},"parent_tool_use_id":"toolu_task"}`}, nil, false)
	for _, e := range evs {
		if e.Verb == chatstream.VerbPartStart && string(e.Meta["parent_tool_use_id"]) != `"toolu_task"` {
			t.Errorf("meta = %v", e.Meta)
		}
	}
}

func TestUserTextAndUnknownLinesAreRaw(t *testing.T) {
	evs := decode(t, chatstream.DecodeOptions{}, []string{initLine,
		`{"type":"user","message":{"role":"user","content":"a plain prompt"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"replayed"}]}}`,
		`{"type":"brand_new","x":1}`,
		`{"type":"stream_event","event":{"type":"future_event"}}`,
		`{"type":"stream_event"}`,
	}, nil, false)
	var types []string
	for _, e := range evs {
		if e.Verb == chatstream.VerbRaw {
			types = append(types, e.Raw.Type)
		}
	}
	want := "user user brand_new stream_event.future_event stream_event"
	if strings.Join(types, " ") != want {
		t.Errorf("raw types = %v, want %s", types, want)
	}
}

// No result line: the process died, was killed, or the pipe closed. That is an
// error, whatever the last line said.
func TestTruncationIsNeverSuccess(t *testing.T) {
	stream := []string{initLine,
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"m1"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"n","input":{}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}}`,
	}
	for n := 0; n <= len(stream); n++ {
		for _, cause := range []error{nil, io.ErrUnexpectedEOF} {
			evs := decode(t, chatstream.DecodeOptions{}, stream[:n], cause, true)
			terms := terminals(evs)
			if len(terms) != 1 || terms[0].Verb != chatstream.VerbRunError || terms[0].Code != chatstream.CodeUpstreamTruncated || !terms[0].Retryable {
				t.Errorf("after %d lines: %+v", n, terms)
				continue
			}
			conformance.Check(t, evs)
		}
	}
	// a stream that already showed its message_stop but never sent result is still truncated
	evs := decode(t, chatstream.DecodeOptions{}, []string{initLine,
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"m1"}}}`,
		`{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"}}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`}, nil, true)
	if terms := terminals(evs); len(terms) != 1 || terms[0].Code != chatstream.CodeUpstreamTruncated {
		t.Errorf("message_stop is not the end of the run: %+v", terms)
	}
}

func TestBlankLinesAreIgnoredAndDecodeAfterCloseFails(t *testing.T) {
	d := claudejson.New().NewDecoder(chatstream.DecodeOptions{})
	if evs, err := d.Decode(chatstream.Frame{Data: []byte("  ")}); err != nil || len(evs) != 0 {
		t.Errorf("blank: %v %v", evs, err)
	}
	d.Close(nil)
	if _, err := d.Decode(chatstream.Frame{Data: []byte(`{}`)}); !errors.Is(err, chatstream.ErrDecoderClosed) {
		t.Errorf("err = %v", err)
	}
}

func TestMalformedLineIsRawAndDoesNotEndTheRun(t *testing.T) {
	evs := decode(t, chatstream.DecodeOptions{}, []string{initLine, `{"type":"assist`, `{"type":"assistant","message":{"id":"a","content":[{"type":"text","text":"ok"}]}}`, result(`"subtype":"success"`)}, nil, true)
	var raw *chatstream.Event
	for i := range evs {
		if evs[i].Verb == chatstream.VerbRaw {
			raw = &evs[i]
		}
	}
	if raw == nil || raw.Raw.Type != "malformed" || string(raw.Raw.Payload) != `"{\"type\":\"assist"` {
		t.Fatalf("raw = %+v", raw)
	}
	if _, err := json.Marshal(raw); err != nil {
		t.Error(err)
	}
	if last(evs).Verb != chatstream.VerbRunFinish {
		t.Errorf("last = %+v", last(evs))
	}
}

func partStarts(evs []chatstream.Event, kind chatstream.PartKind) []chatstream.Event {
	var out []chatstream.Event
	for _, e := range evs {
		if e.Verb == chatstream.VerbPartStart && e.PartKind() == kind {
			out = append(out, e)
		}
	}
	return out
}

// A tool_result names the tool_call part it answers, by that part's id, and says
// when the tool failed. The convention is enforced by conformance.Validate.
func TestToolResultNamesItsCallPart(t *testing.T) {
	evs := decode(t, chatstream.DecodeOptions{}, []string{initLine,
		`{"type":"assistant","message":{"id":"m","content":[{"type":"text","text":"hi"},{"type":"tool_use","id":"tu1","name":"Bash","input":{"c":1}},{"type":"tool_use","id":"tu2","name":"Read","input":{}}]}}`,
		`{"type":"user","uuid":"u1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu2","content":"nope","is_error":true},{"type":"tool_result","tool_use_id":"tu1","content":"fine"}]}}`,
		result(`"subtype":"success"`)}, nil, true)
	conformance.Check(t, evs)
	calls := map[string]string{} // upstream id -> part id
	for _, c := range partStarts(evs, chatstream.PartToolCall) {
		calls[strings.Trim(string(c.Meta["id"]), `"`)] = c.PartID
		if c.MetaString(chatstream.MetaName) == "" {
			t.Errorf("tool_call without a name: %+v", c.Meta)
		}
	}
	results := partStarts(evs, chatstream.PartToolResult)
	if len(results) != 2 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].MetaString(chatstream.MetaCallID) != calls["tu2"] || !results[0].MetaBool(chatstream.MetaIsError) {
		t.Errorf("result 0 = %+v, want call_id %s and is_error", results[0].Meta, calls["tu2"])
	}
	if results[1].MetaString(chatstream.MetaCallID) != calls["tu1"] || results[1].MetaBool(chatstream.MetaIsError) {
		t.Errorf("result 1 = %+v, want call_id %s and no is_error", results[1].Meta, calls["tu1"])
	}
	if calls["tu1"] == "" || calls["tu1"] == calls["tu2"] {
		t.Errorf("calls = %v", calls)
	}
}

// The same holds when the call streamed and the result arrives whole.
func TestStreamedCallIsNamedByALaterResult(t *testing.T) {
	evs := decode(t, chatstream.DecodeOptions{}, []string{initLine,
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"ms"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tuS","name":"Bash","input":{}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tuS","content":"ok"}]}}`,
		result(`"subtype":"success"`)}, nil, true)
	conformance.Check(t, evs)
	rs := partStarts(evs, chatstream.PartToolResult)
	if len(rs) != 1 || rs[0].MetaString(chatstream.MetaCallID) != "ms:0" {
		t.Fatalf("results = %+v", rs)
	}
}

// A result for a call the stream never showed cannot name it. It is kept as a
// data part, not a tool_result with a dangling call_id.
func TestOrphanResultIsADataPart(t *testing.T) {
	evs := decode(t, chatstream.DecodeOptions{}, []string{initLine,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"ghost","content":"x"}]}}`,
		result(`"subtype":"success"`)}, nil, true)
	conformance.Check(t, evs)
	if len(partStarts(evs, chatstream.PartToolResult)) != 0 {
		t.Fatal("an orphan result became a tool_result part")
	}
	data := partStarts(evs, chatstream.PartData)
	if len(data) != 1 || data[0].MetaString("tool_use_id") != "ghost" {
		t.Fatalf("data parts = %+v", data)
	}
}

// A result that arrives before its call (out of order) is an orphan too.
func TestResultBeforeItsCallIsAnOrphan(t *testing.T) {
	evs := decode(t, chatstream.DecodeOptions{}, []string{initLine,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"late","content":"x"}]}}`,
		`{"type":"assistant","message":{"id":"m","content":[{"type":"tool_use","id":"late","name":"Bash","input":{}}]}}`,
		result(`"subtype":"success"`)}, nil, true)
	conformance.Check(t, evs)
	if len(partStarts(evs, chatstream.PartToolResult)) != 0 {
		t.Fatal("a result before its call must not claim it")
	}
}

func TestApprovalCallIDIsThePartID(t *testing.T) {
	fx, err := conformance.LoadFixture("testdata/approval.frames.json")
	if err != nil {
		t.Fatal(err)
	}
	evs, _ := conformance.DecodeFixture(claudejson.New(), fx)
	calls := partStarts(evs, chatstream.PartToolCall)
	for _, e := range evs {
		if e.Verb == chatstream.VerbApprovalRequest && e.ApprovalID == "req_1" && e.CallID != calls[0].PartID {
			t.Errorf("CallID = %q, want the part id %q", e.CallID, calls[0].PartID)
		}
	}
	// an approval for an unseen call names none
	fx, _ = conformance.LoadFixture("testdata/orphan_result.frames.json")
	evs, _ = conformance.DecodeFixture(claudejson.New(), fx)
	for _, e := range evs {
		if e.Verb == chatstream.VerbApprovalRequest && e.CallID != "" {
			t.Errorf("CallID = %q for a call never seen", e.CallID)
		}
	}
}
