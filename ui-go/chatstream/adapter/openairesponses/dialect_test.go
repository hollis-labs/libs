package openairesponses_test

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui-go/chatstream/adapter/openairesponses"
	"github.com/hollis-labs/libs/ui-go/chatstream/conformance"
)

func newDec() chatstream.Decoder {
	return openairesponses.New().NewDecoder(conformance.DecodeOptions())
}

func feed(t *testing.T, d chatstream.Decoder, datas ...string) []chatstream.Event {
	t.Helper()
	var out []chatstream.Event
	for _, s := range datas {
		evs, err := d.Decode(chatstream.Frame{Data: []byte(s)})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, evs...)
	}
	return out
}

func run(t *testing.T, datas ...string) []chatstream.Event {
	t.Helper()
	d := newDec()
	out := feed(t, d, datas...)
	return append(out, d.Close(nil)...)
}

func last(evs []chatstream.Event) chatstream.Event { return evs[len(evs)-1] }

func verbs(evs []chatstream.Event) string {
	var v []string
	for _, e := range evs {
		v = append(v, string(e.Verb))
	}
	return strings.Join(v, " ")
}

const created = `{"type":"response.created","response":{"id":"resp_1","model":"m","status":"in_progress"}}`

func completed(resp string) string {
	return `{"type":"response.completed","response":` + resp + `}`
}

// The 59 events of the reference, each with a payload and what a decoder that
// has just seen response.created produces for it. Every event has a defined
// disposition; none is silently dropped except the ones documented as
// carrying nothing this vocabulary needs.
var dispositions = []struct {
	typ, payload, want string
}{
	{"response.queued", `{}`, "activity"},
	{"response.in_progress", `{"response":{}}`, ""},
	{"response.completed", `{"response":{"status":"completed"}}`, "message.end run.finish"},
	{"response.failed", `{"response":{"status":"failed","error":{"code":"server_error","message":"x"}}}`, "message.end run.error"},
	{"response.incomplete", `{"response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`, "message.end run.finish"},
	{"error", `{"code":"server_error","message":"x"}`, "message.end run.error"},
	{"response.output_item.added", `{"output_index":0,"item":{"id":"m1","type":"message","role":"assistant"}}`, ""},
	{"response.output_item.done", `{"output_index":0,"item":{"id":"m1","type":"message","role":"assistant"}}`, ""},
	{"response.content_part.added", `{"item_id":"m1","content_index":0,"part":{"type":"output_text","text":""}}`, "part.start"},
	{"response.content_part.done", `{"item_id":"m1","content_index":0,"part":{"type":"output_text"}}`, ""},
	{"response.output_text.delta", `{"item_id":"m1","content_index":0,"delta":"x"}`, "part.start part.delta"},
	{"response.output_text.done", `{"item_id":"m1","content_index":0,"text":"x"}`, ""},
	{"response.output_text.annotation.added", `{"item_id":"m1","content_index":0,"annotation_index":0,"annotation":{"type":"file_path","file_id":"f","index":1}}`, "part.start part.end"},
	{"response.refusal.delta", `{"item_id":"m1","content_index":0,"delta":"no"}`, "part.start part.delta"},
	{"response.refusal.done", `{"item_id":"m1","content_index":0,"refusal":"no"}`, ""},
	{"response.function_call_arguments.delta", `{"item_id":"f1","delta":"{}"}`, "part.start part.delta"},
	{"response.function_call_arguments.done", `{"item_id":"f1","arguments":"{}"}`, "part.start part.delta part.end"},
	{"response.custom_tool_call_input.delta", `{"item_id":"c1","delta":"x"}`, "part.start part.delta"},
	{"response.custom_tool_call_input.done", `{"item_id":"c1","input":"x"}`, "part.start part.delta part.end"},
	{"response.mcp_call_arguments.delta", `{"item_id":"k1","delta":"{}"}`, "part.start part.delta"},
	{"response.mcp_call_arguments.done", `{"item_id":"k1","arguments":"{}"}`, "part.start part.delta part.end"},
	{"response.reasoning_summary_part.added", `{"item_id":"r1","summary_index":0,"part":{"type":"summary_text","text":""}}`, "part.start"},
	{"response.reasoning_summary_part.done", `{"item_id":"r1","summary_index":0,"part":{}}`, ""},
	{"response.reasoning_summary_text.delta", `{"item_id":"r1","summary_index":0,"delta":"x"}`, "part.start part.delta"},
	{"response.reasoning_summary_text.done", `{"item_id":"r1","summary_index":0,"text":"x"}`, ""},
	{"response.reasoning_text.delta", `{"item_id":"r1","content_index":0,"delta":"x"}`, "part.start part.delta"},
	{"response.reasoning_text.done", `{"item_id":"r1","content_index":0,"text":"x"}`, ""},
	{"response.file_search_call.in_progress", `{"item_id":"i","output_index":0}`, "activity"},
	{"response.file_search_call.searching", `{"item_id":"i","output_index":0}`, "activity"},
	{"response.file_search_call.completed", `{"item_id":"i","output_index":0}`, "activity"},
	{"response.web_search_call.in_progress", `{"item_id":"i","output_index":0}`, "activity"},
	{"response.web_search_call.searching", `{"item_id":"i","output_index":0}`, "activity"},
	{"response.web_search_call.completed", `{"item_id":"i","output_index":0}`, "activity"},
	{"response.code_interpreter_call.in_progress", `{"item_id":"i","output_index":0}`, "activity"},
	{"response.code_interpreter_call.interpreting", `{"item_id":"i","output_index":0}`, "activity"},
	{"response.code_interpreter_call.completed", `{"item_id":"i","output_index":0}`, "activity"},
	{"response.code_interpreter_call_code.delta", `{"item_id":"i","delta":"x"}`, "raw"},
	{"response.code_interpreter_call_code.done", `{"item_id":"i","code":"x"}`, "raw"},
	{"response.image_generation_call.in_progress", `{"item_id":"i","output_index":0}`, "activity"},
	{"response.image_generation_call.generating", `{"item_id":"i","output_index":0}`, "activity"},
	{"response.image_generation_call.completed", `{"item_id":"i","output_index":0}`, "activity"},
	{"response.image_generation_call.partial_image", `{"item_id":"i","partial_image_index":0,"partial_image_b64":"AA=="}`, "raw"},
	{"response.mcp_call.in_progress", `{"item_id":"i","output_index":0}`, "activity"},
	{"response.mcp_call.completed", `{"item_id":"i","output_index":0}`, "activity"},
	{"response.mcp_call.failed", `{"item_id":"i","output_index":0}`, "activity"},
	{"response.mcp_list_tools.in_progress", `{"item_id":"i","output_index":0}`, "activity"},
	{"response.mcp_list_tools.completed", `{"item_id":"i","output_index":0}`, "activity"},
	{"response.mcp_list_tools.failed", `{"item_id":"i","output_index":0}`, "activity"},
	{"response.shell_call_command.added", `{"output_index":0,"command_index":0,"command":"ls"}`, "raw"},
	{"response.shell_call_command.delta", `{"output_index":0,"command_index":0,"delta":"l"}`, "raw"},
	{"response.shell_call_command.done", `{"output_index":0,"command_index":0,"command":"ls"}`, "raw"},
	{"response.shell_call_output_content.delta", `{"item_id":"i","output_index":0,"command_index":0,"delta":{"stdout":"x"}}`, "raw"},
	{"response.shell_call_output_content.done", `{"item_id":"i","output_index":0,"command_index":0,"output":[]}`, "raw"},
	{"response.audio.delta", `{"delta":"AA=="}`, "raw"},
	{"response.audio.done", `{}`, "raw"},
	{"response.audio.transcript.delta", `{"delta":"hi"}`, "raw"},
	{"response.audio.transcript.done", `{}`, "raw"},
	{"response.compaction.compacting", `{"output_index":0,"item_id":"i"}`, "activity"},
}

// documented are the 59 event types of the reference; response.created is
// tested on its own because it is the one that starts a run.
var documented = strings.Fields(`response.created response.queued response.in_progress response.completed response.failed
response.incomplete error response.output_item.added response.output_item.done response.content_part.added
response.content_part.done response.output_text.delta response.output_text.done response.output_text.annotation.added
response.refusal.delta response.refusal.done response.function_call_arguments.delta response.function_call_arguments.done
response.custom_tool_call_input.delta response.custom_tool_call_input.done response.reasoning_summary_part.added
response.reasoning_summary_part.done response.reasoning_summary_text.delta response.reasoning_summary_text.done
response.reasoning_text.delta response.reasoning_text.done response.file_search_call.in_progress
response.file_search_call.searching response.file_search_call.completed response.web_search_call.in_progress
response.web_search_call.searching response.web_search_call.completed response.code_interpreter_call.in_progress
response.code_interpreter_call.interpreting response.code_interpreter_call.completed response.code_interpreter_call_code.delta
response.code_interpreter_call_code.done response.image_generation_call.in_progress response.image_generation_call.generating
response.image_generation_call.completed response.image_generation_call.partial_image response.mcp_call.in_progress
response.mcp_call.completed response.mcp_call.failed response.mcp_call_arguments.delta response.mcp_call_arguments.done
response.mcp_list_tools.in_progress response.mcp_list_tools.completed response.mcp_list_tools.failed
response.shell_call_command.added response.shell_call_command.delta response.shell_call_command.done
response.shell_call_output_content.delta response.shell_call_output_content.done response.audio.delta response.audio.done
response.audio.transcript.delta response.audio.transcript.done response.compaction.compacting`)

func TestEveryDocumentedEventHasADisposition(t *testing.T) {
	if len(documented) != 59 {
		t.Fatalf("the reference lists 59 events, this test lists %d", len(documented))
	}
	covered := map[string]bool{"response.created": true}
	for _, tc := range dispositions {
		covered[tc.typ] = true
		payload := strings.TrimSuffix(tc.payload, "}")
		if payload == "{" {
			payload = `{"type":"` + tc.typ + `"}`
		} else {
			payload = payload + `,"type":"` + tc.typ + `"}`
		}
		d := newDec()
		feed(t, d, created)
		got := verbs(feed(t, d, payload))
		if got != tc.want {
			t.Errorf("%s: produced [%s], want [%s]", tc.typ, got, tc.want)
		}
	}
	var missing []string
	for _, typ := range documented {
		if !covered[typ] {
			missing = append(missing, typ)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("events with no disposition test: %v", missing)
	}
}

func TestCreatedStartsTheRunOnceAndASecondIsRaw(t *testing.T) {
	d := newDec()
	first := feed(t, d, created)
	if verbs(first) != "run.start message.start" {
		t.Fatalf("created: %s", verbs(first))
	}
	if second := feed(t, d, created); verbs(second) != "raw" {
		t.Fatalf("a second response.created must be preserved raw, got %s", verbs(second))
	}
}

// The incomplete_details.reason table, every documented value plus the legacy
// word the reference example uses and anything unknown.
func TestIncompleteReasonTable(t *testing.T) {
	table := map[string]chatstream.FinishReason{
		"max_output_tokens": chatstream.FinishLength,
		"max_tokens":        chatstream.FinishLength,
		"max_messages":      chatstream.FinishTurnLimit,
		"content_filter":    chatstream.FinishContentFilter,
		"steered":           chatstream.FinishOther,
		"something_new":     chatstream.FinishOther,
	}
	for raw, want := range table {
		evs := run(t, created, `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"`+raw+`"}}}`)
		fin := last(evs)
		if fin.Verb != chatstream.VerbRunFinish || fin.Finish() != want || fin.RawReason != raw {
			t.Errorf("reason %q: %s %q raw %q, want %q", raw, fin.Verb, fin.Reason, fin.RawReason, want)
		}
	}
	// no details at all
	fin := last(run(t, created, `{"type":"response.incomplete","response":{"status":"incomplete"}}`))
	if fin.Verb != chatstream.VerbRunFinish || fin.Finish() != chatstream.FinishOther || fin.RawReason != "" {
		t.Errorf("no details: %+v", fin)
	}
}

// response.error.code -> retryable, for every documented ResponseErrorCode.
func TestErrorCodeRetryTable(t *testing.T) {
	retryable := map[string]bool{"server_error": true, "rate_limit_exceeded": true, "vector_store_timeout": true}
	codes := strings.Fields(`server_error rate_limit_exceeded invalid_prompt data_residency_mismatch bio_policy
misalignment_policy_violation vector_store_timeout invalid_image invalid_image_format invalid_base64_image invalid_image_url
image_too_large image_too_small image_parse_error image_content_policy_violation invalid_image_mode image_file_too_large
unsupported_image_media_type empty_image_file failed_to_download_image image_file_not_found`)
	if len(codes) != 21 {
		t.Fatalf("ResponseErrorCode lists 21 values, this test has %d", len(codes))
	}
	for _, code := range codes {
		for name, frame := range map[string]string{
			"response.failed": `{"type":"response.failed","response":{"status":"failed","error":{"code":"` + code + `","message":"m"}}}`,
			"error event":     `{"type":"error","code":"` + code + `","message":"m"}`,
		} {
			fin := last(run(t, created, frame))
			if fin.Verb != chatstream.VerbRunError || fin.Code != code || fin.Retryable != retryable[code] {
				t.Errorf("%s %s: %s code %q retryable %v, want retryable %v", name, code, fin.Verb, fin.Code, fin.Retryable, retryable[code])
			}
		}
	}
	// no code at all
	if fin := last(run(t, created, `{"type":"error","message":"m"}`)); fin.Code != chatstream.CodeUpstreamError || fin.Retryable {
		t.Errorf("codeless error = %+v", fin)
	}
}

// Nanite's failure matrix: a completed event whose response carries an error or
// a failed status is still a failure; a cancel status is an abort.
func TestCompletedEventThatIsReallyAFailureOrCancel(t *testing.T) {
	cases := map[string]struct {
		body string
		verb chatstream.Verb
	}{
		"error object":             {`{"status":"completed","error":{"code":"server_error","message":"boom"}}`, chatstream.VerbRunError},
		"status failed":            {`{"status":"failed"}`, chatstream.VerbRunError},
		"status is the cancel one": {`{"status":"cancelled"}`, chatstream.VerbRunAbort}, //nolint:misspell // the upstream status value
		"status incomplete":        {`{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`, chatstream.VerbRunFinish},
	}
	for name, tc := range cases {
		fin := last(run(t, created, completed(tc.body)))
		if fin.Verb != tc.verb {
			t.Errorf("%s: %s, want %s", name, fin.Verb, tc.verb)
		}
	}
	if fin := last(run(t, created, completed(`{"status":"failed"}`))); fin.Code != chatstream.CodeUpstreamError || fin.Message == "" {
		t.Errorf("failure with no error object: %+v", fin)
	}
}

// input_tokens includes cached tokens and output_tokens includes reasoning
// tokens: the components must add up to input+output once.
func TestUsageIsDisjointNotDoubleCounted(t *testing.T) {
	fin := last(run(t, created, completed(`{"status":"completed","usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":600,"cache_write_tokens":100},"output_tokens":200,"output_tokens_details":{"reasoning_tokens":50},"total_tokens":1200}}`)))
	u := fin.Usage
	if u == nil || u.Scope != chatstream.UsageFinal {
		t.Fatalf("usage = %+v", u)
	}
	if u.UncachedInput != 300 || u.CacheRead != 600 || u.CacheWrite != 100 || u.Output != 150 || u.Reasoning != 50 {
		t.Errorf("components = %+v", u)
	}
	if u.Total() != 1200 {
		t.Errorf("Total = %d, want input+output = 1200", u.Total())
	}
	if naive := 1000 + 600 + 100 + 200; naive == u.Total() {
		t.Error("vacuous: the naive sum equals Total")
	}
	// usage on a failed response is not lost
	evs := run(t, created, `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"m"},"usage":{"input_tokens":5,"output_tokens":1}}}`)
	if !strings.Contains(verbs(evs), "usage run.error") {
		t.Errorf("usage before failure: %s", verbs(evs))
	}
}

func TestSequenceNumberIsExtNeverSeq(t *testing.T) {
	evs := run(t, `{"type":"response.created","sequence_number":7,"response":{"id":"r","status":"in_progress"}}`,
		`{"type":"response.output_text.delta","sequence_number":8,"item_id":"m","content_index":0,"delta":"x"}`,
		`{"type":"response.output_text.delta","item_id":"m","content_index":0,"delta":"y"}`,
		completed(`{"status":"completed"}`))
	var withSeq, without int
	for _, e := range evs {
		if e.Seq != 0 {
			t.Errorf("Event.Seq must stay hub-assigned, got %d", e.Seq)
		}
		var ext struct {
			N *int `json:"sequence_number"`
		}
		_ = json.Unmarshal(e.Ext["openai"], &ext)
		if ext.N != nil {
			withSeq++
		} else {
			without++
		}
	}
	if withSeq == 0 || without == 0 {
		t.Errorf("with sequence_number %d, without %d: frames that carry it stamp their events, others do not", withSeq, without)
	}
	if evs[0].Verb != chatstream.VerbRunStart {
		t.Fatal(verbs(evs))
	}
	var ext struct {
		N int `json:"sequence_number"`
	}
	_ = json.Unmarshal(evs[0].Ext["openai"], &ext)
	if ext.N != 7 {
		t.Errorf("run.start sequence_number = %d", ext.N)
	}
}

func TestFinishReasonIsToolCallsOnlyForFunctionAndCustomCalls(t *testing.T) {
	call := `{"type":"function_call","id":"f","call_id":"c","name":"n","arguments":"{}"}`
	mcp := `{"type":"mcp_call","id":"k","name":"n","arguments":"{}"}`
	tests := []struct {
		name   string
		frames []string
		want   chatstream.FinishReason
	}{
		{"function_call in output", []string{created, completed(`{"status":"completed","output":[` + call + `]}`)}, chatstream.FinishToolCalls},
		{"mcp_call in output", []string{created, completed(`{"status":"completed","output":[` + mcp + `]}`)}, chatstream.FinishStop},
		{"text only", []string{created, completed(`{"status":"completed","output":[{"type":"message","id":"m"}]}`)}, chatstream.FinishStop},
		// the final output is authoritative when present
		{"streamed call, output without it", []string{created,
			`{"type":"response.output_item.added","item":` + call + `}`,
			completed(`{"status":"completed","output":[{"type":"message","id":"m"}]}`)}, chatstream.FinishStop},
		// without a final output, what streamed decides
		{"streamed call, no output list", []string{created,
			`{"type":"response.output_item.added","item":` + call + `}`,
			completed(`{"status":"completed"}`)}, chatstream.FinishToolCalls},
	}
	for _, tc := range tests {
		if fin := last(run(t, tc.frames...)); fin.Finish() != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, fin.Reason, tc.want)
		}
	}
}

func TestToolCallPartIDsAndMeta(t *testing.T) {
	evs := run(t, created,
		`{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"n","arguments":""}}`,
		`{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_2","name":"m","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_2","delta":"{}"}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"a\":1}"}`,
		completed(`{"status":"completed"}`))
	final := map[string]string{}
	for _, e := range evs {
		if e.Verb == chatstream.VerbPartEnd {
			final[e.PartID] = string(e.Final)
		}
		if e.Verb == chatstream.VerbPartStart {
			var m map[string]any
			for k, v := range e.Meta {
				if m == nil {
					m = map[string]any{}
				}
				var x any
				_ = json.Unmarshal(v, &x)
				m[k] = x
			}
			if e.PartID == "call_1" && (m["name"] != "n" || m["item_id"] != "fc_1" || m["call_id"] != "call_1") {
				t.Errorf("meta = %v", m)
			}
		}
	}
	// call_id names the part when present, the item id otherwise; deltas reach the right part by item id
	if final["call_1"] != `{"a":1}` || final["fc_2"] != `{}` {
		t.Errorf("finals = %v (verbs: %s)", final, verbs(evs))
	}
}

func TestMCPCallErrorBecomesAnErrorToolResult(t *testing.T) {
	evs := run(t, created,
		`{"type":"response.output_item.added","item":{"type":"mcp_call","id":"k","name":"n","server_label":"s"}}`,
		`{"type":"response.output_item.done","item":{"type":"mcp_call","id":"k","name":"n","server_label":"s","arguments":"{}","error":"boom"}}`,
		completed(`{"status":"completed"}`))
	var res *chatstream.Event
	for i := range evs {
		if evs[i].Verb == chatstream.VerbPartStart && evs[i].Kind == string(chatstream.PartToolResult) {
			res = &evs[i]
		}
	}
	if res == nil || string(res.Meta["is_error"]) != "true" {
		t.Fatalf("tool_result = %+v", res)
	}
}

func TestOpaqueReasoningItemIsCapturedVerbatim(t *testing.T) {
	item := `{"id":"rs_9","type":"reasoning","summary":[],"encrypted_content":"ENC=="}`
	evs := run(t, created, `{"type":"response.output_item.done","item":`+item+`}`, completed(`{"status":"completed"}`))
	var final string
	for _, e := range evs {
		if e.Verb == chatstream.VerbPartEnd && e.PartID == "rs_9/opaque" {
			final = string(e.Final)
		}
	}
	if final != item {
		t.Errorf("final = %s, want the whole item for round-trip replay", final)
	}
	// a reasoning item with no encrypted content yields no opaque part
	if strings.Contains(verbs(run(t, created, `{"type":"response.output_item.done","item":{"id":"rs_0","type":"reasoning","summary":[]}}`, completed(`{"status":"completed"}`))), "part.start") {
		t.Error("no encrypted_content means nothing to capture")
	}
}

// Truncation: the stream ends before response.completed / incomplete / failed.
func TestTruncationCases(t *testing.T) {
	tests := []struct {
		name   string
		frames []string
	}{
		{"no frames", nil},
		{"after created", []string{created}},
		{"mid text", []string{created, `{"type":"response.output_text.delta","item_id":"m","content_index":0,"delta":"par"}`}},
		{"mid tool arguments", []string{created,
			`{"type":"response.output_item.added","item":{"type":"function_call","id":"f","call_id":"c","name":"n"}}`,
			`{"type":"response.function_call_arguments.delta","item_id":"f","delta":"{\"x\":"}`}},
		{"between items", []string{created,
			`{"type":"response.output_item.added","item":{"id":"m","type":"message"}}`,
			`{"type":"response.output_text.delta","item_id":"m","content_index":0,"delta":"done"}`,
			`{"type":"response.output_item.done","item":{"id":"m","type":"message"}}`}},
		{"after [DONE], which is not a terminal signal here", []string{created,
			`{"type":"response.output_text.delta","item_id":"m","content_index":0,"delta":"x"}`, "[DONE]"}},
	}
	for _, tc := range tests {
		evs := run(t, tc.frames...)
		fin := last(evs)
		if fin.Verb != chatstream.VerbRunError || fin.Code != chatstream.CodeUpstreamTruncated || !fin.Retryable {
			t.Errorf("%s: last = %s %q retryable=%v", tc.name, fin.Verb, fin.Code, fin.Retryable)
		}
		conformance.Check(t, evs)
		if err := conformance.CheckReplayEquivalence(evs); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	// the open tool call is finalized from what streamed: invalid JSON gets no Final
	evs := run(t, created,
		`{"type":"response.output_item.added","item":{"type":"function_call","id":"f","call_id":"c","name":"n"}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"f","delta":"{}"}`)
	for _, e := range evs {
		if e.Verb == chatstream.VerbPartEnd && e.PartID == "c" && string(e.Final) != "{}" {
			t.Errorf("open tool call closed with final %q, want {}", e.Final)
		}
	}
}

func TestNothingAfterTheTerminalEvent(t *testing.T) {
	d := newDec()
	feed(t, d, created, completed(`{"status":"completed"}`))
	late := feed(t, d, `{"type":"response.output_text.delta","item_id":"m","content_index":0,"delta":"late"}`, `{"type":"error","code":"server_error","message":"x"}`)
	if len(late) != 0 {
		t.Fatalf("events after the terminal event: %s", verbs(late))
	}
	if got := d.Close(nil); len(got) != 0 {
		t.Fatalf("Close after the terminal event: %s", verbs(got))
	}
}

func TestFrameEventNameIsUsedWhenTheJSONHasNoType(t *testing.T) {
	d := newDec()
	feed(t, d, created)
	evs, err := d.Decode(chatstream.Frame{Event: "response.output_text.delta", Data: []byte(`{"item_id":"m","content_index":0,"delta":"x"}`)})
	if err != nil || verbs(evs) != "part.start part.delta" {
		t.Fatalf("%v %s", err, verbs(evs))
	}
	// neither: malformed, not terminal
	evs, _ = d.Decode(chatstream.Frame{Data: []byte(`{"item_id":"m"}`)})
	if verbs(evs) != "raw" || evs[0].Raw.Type != "malformed" {
		t.Errorf("typeless frame: %s", verbs(evs))
	}
}

func TestRunIDDefaultsAndOptions(t *testing.T) {
	d := openairesponses.New().NewDecoder(chatstream.DecodeOptions{Provider: "azure", Model: "fallback"})
	evs, _ := d.Decode(chatstream.Frame{Data: []byte(`{"type":"response.created","response":{"id":"resp_77"}}`)})
	if evs[0].RunID != "resp_77" || evs[0].Provider != "azure" || evs[0].Model != "fallback" || evs[1].MessageID != "resp_77" {
		t.Errorf("%+v %+v", evs[0], evs[1])
	}
	for _, e := range evs {
		if e.RunID != "resp_77" || e.Time.IsZero() {
			t.Errorf("unstamped %+v", e)
		}
	}
}

func TestDecodeAfterCloseIsAnError(t *testing.T) {
	d := newDec()
	d.Close(nil)
	if _, err := d.Decode(chatstream.Frame{Data: []byte(created)}); !errors.Is(err, chatstream.ErrDecoderClosed) {
		t.Fatalf("err = %v", err)
	}
}

func TestUnknownEventIsRawAndNeverTerminal(t *testing.T) {
	evs := run(t, created, `{"type":"response.something.new","x":1}`, completed(`{"status":"completed"}`))
	if !strings.Contains(verbs(evs), "raw") || last(evs).Verb != chatstream.VerbRunFinish {
		t.Fatalf("%s", verbs(evs))
	}
}

func TestToolMetaNameAndResultCorrelation(t *testing.T) {
	evs := run(t, created,
		`{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"weather","arguments":""}}`,
		`{"type":"response.output_item.added","item":{"type":"custom_tool_call","id":"ct_1","call_id":"call_2","name":"sql","input":""}}`,
		`{"type":"response.output_item.added","item":{"type":"mcp_call","id":"mcp_1","name":"search","server_label":"docs"}}`,
		`{"type":"response.output_item.done","item":{"type":"mcp_call","id":"mcp_1","name":"search","server_label":"docs","arguments":"{}","output":"ok"}}`,
		`{"type":"response.output_item.done","item":{"type":"mcp_call","id":"mcp_2","name":"broken","server_label":"docs","arguments":"{}","error":"boom"}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"orphan","delta":"{}"}`,
		completed(`{"status":"completed"}`))
	names, results := map[string]string{}, map[string]*chatstream.Event{}
	for i, e := range evs {
		if e.Verb != chatstream.VerbPartStart {
			continue
		}
		switch e.Kind {
		case string(chatstream.PartToolCall):
			names[e.PartID] = e.MetaString(chatstream.MetaName)
		case string(chatstream.PartToolResult):
			results[e.PartID] = &evs[i]
		}
	}
	want := map[string]string{"call_1": "weather", "call_2": "sql", "mcp_1": "search", "mcp_2": "broken", "orphan": "unknown"}
	for id, n := range want {
		if names[id] != n {
			t.Errorf("tool_call %s name = %q, want %q (all: %v)", id, names[id], n, names)
		}
	}
	// a result names the PART ID of the call it answers, not the upstream call id
	for id, wantErr := range map[string]bool{"mcp_1/result": false, "mcp_2/result": true} {
		r := results[id]
		if r == nil {
			t.Fatalf("no result %s in %v", id, verbs(evs))
		}
		if got := r.MetaString(chatstream.MetaCallID); got != strings.TrimSuffix(id, "/result") {
			t.Errorf("%s call_id = %q", id, got)
		}
		if r.MetaBool(chatstream.MetaIsError) != wantErr {
			t.Errorf("%s is_error = %v", id, r.MetaBool(chatstream.MetaIsError))
		}
	}
	conformance.Check(t, evs)
}
