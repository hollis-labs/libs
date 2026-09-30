package anthropic_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/adapter/anthropic"
	"github.com/hollis-labs/go-chatstream/conformance"
	"github.com/hollis-labs/go-chatstream/internal/anthropicwire"
)

func TestCapabilitiesAreValid(t *testing.T) {
	a := anthropic.New()
	if err := a.Capabilities().Validate(); err != nil {
		t.Fatal(err)
	}
	if a.Name() != "anthropic.messages" {
		t.Errorf("Name = %q", a.Name())
	}
}

func TestFixtures(t *testing.T) { conformance.CheckDecoderDir(t, anthropic.New(), "testdata") }

// Every prefix of every recorded stream, ended cleanly or broken, is well formed
// and ends in exactly one terminal event that is never a success.
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
		t.Run(fx.Name, func(t *testing.T) { conformance.CheckTruncation(t, anthropic.New(), fx) })
	}
}

// ---- dialect tests ----

type fr struct{ event, data string }

func decode(t *testing.T, opts chatstream.DecodeOptions, frames []fr, cause error, closeIt bool) []chatstream.Event {
	t.Helper()
	if opts.Now == nil {
		opts.Now = func() time.Time { return conformance.FixedTime }
	}
	d := anthropic.New().NewDecoder(opts)
	var out []chatstream.Event
	for i, f := range frames {
		evs, err := d.Decode(chatstream.Frame{Event: f.event, Data: []byte(f.data)})
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		out = append(out, evs...)
	}
	if closeIt {
		out = append(out, d.Close(cause)...)
	}
	return out
}

func stream(stop, usage string, extraDelta string) []fr {
	return []fr{
		{"message_start", `{"type":"message_start","message":{"id":"m1","role":"assistant","model":"mod","usage":{"input_tokens":1,"output_tokens":1}}}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":` + stop + `,"stop_sequence":null` + extraDelta + `},"usage":` + usage + `}`},
		{"message_stop", `{"type":"message_stop"}`},
	}
}

func last(evs []chatstream.Event) chatstream.Event { return evs[len(evs)-1] }

func terminals(evs []chatstream.Event) []chatstream.Event {
	var out []chatstream.Event
	for _, e := range evs {
		if e.IsTerminal() {
			out = append(out, e)
		}
	}
	return out
}

// Every documented stop_reason, with the raw word kept beside the mapped one.
func TestStopReasonTable(t *testing.T) {
	table := []struct {
		raw  string
		want chatstream.FinishReason
	}{
		{"end_turn", chatstream.FinishStop},
		{"max_tokens", chatstream.FinishLength},
		{"stop_sequence", chatstream.FinishStop},
		{"tool_use", chatstream.FinishToolCalls},
		{"pause_turn", chatstream.FinishPause},
		{"refusal", chatstream.FinishRefusal},
		{"model_context_window_exceeded", chatstream.FinishContextExceeded},
		{"compaction", chatstream.FinishOther},
		{"something_new", chatstream.FinishOther},
	}
	for _, tc := range table {
		evs := decode(t, chatstream.DecodeOptions{}, stream(`"`+tc.raw+`"`, `{"output_tokens":2}`, ""), nil, true)
		fin := last(evs)
		if fin.Verb != chatstream.VerbRunFinish || fin.Finish() != tc.want || fin.RawReason != tc.raw {
			t.Errorf("%s: %s %q raw %q, want %s", tc.raw, fin.Verb, fin.Reason, fin.RawReason, tc.want)
		}
		if got := anthropic.FinishReason(tc.raw); got != tc.want {
			t.Errorf("FinishReason(%q) = %s", tc.raw, got)
		}
	}
	// no stop_reason at all: a message_stop without a message_delta
	evs := decode(t, chatstream.DecodeOptions{}, []fr{
		{"message_start", `{"type":"message_start","message":{"id":"m1"}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}, nil, true)
	if fin := last(evs); fin.Verb != chatstream.VerbRunFinish || fin.Finish() != chatstream.FinishOther || fin.RawReason != "" {
		t.Errorf("no stop reason: %+v", fin)
	}
}

// Anthropic's input_tokens excludes cache reads and writes: the components add up
// to exactly what was billed, with nothing counted twice.
func TestUsageComponentsAreDisjoint(t *testing.T) {
	frames := []fr{
		{"message_start", `{"type":"message_start","message":{"id":"m1","usage":{"input_tokens":400,"cache_creation_input_tokens":100,"cache_read_input_tokens":600,"output_tokens":1}}}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":200}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}
	evs := decode(t, chatstream.DecodeOptions{}, frames, nil, true)
	u := last(evs).Usage
	if u == nil || u.Scope != chatstream.UsageFinal {
		t.Fatalf("final usage = %+v", u)
	}
	if u.UncachedInput != 400 || u.CacheWrite != 100 || u.CacheRead != 600 || u.Output != 200 {
		t.Errorf("components = %+v", u)
	}
	// input 400 + cache write 100 + cache read 600 + output 200 = 1300; adding
	// the cache counts to a total that already had them would give 2000
	if u.Total() != 1300 || u.Input() != 1100 {
		t.Errorf("Total = %d Input = %d", u.Total(), u.Input())
	}
	// message_delta omitted the input counts: they were kept, not reset to zero
	var cum []chatstream.Event
	for _, e := range evs {
		if e.Verb == chatstream.VerbUsage {
			cum = append(cum, e)
		}
	}
	if len(cum) != 2 || cum[1].Usage.UncachedInput != 400 || cum[1].Usage.Output != 200 || cum[1].Usage.Scope != chatstream.UsageCumulative {
		t.Errorf("cumulative usage events = %+v", cum)
	}
}

func TestThinkingTokensAreSplitOutOfOutput(t *testing.T) {
	frames := stream(`"end_turn"`, `{"output_tokens":100,"output_tokens_details":{"thinking_tokens":60}}`, "")
	u := last(decode(t, chatstream.DecodeOptions{}, frames, nil, true)).Usage
	if u.Output != 40 || u.Reasoning != 60 || u.Total() != 101 {
		t.Errorf("usage = %+v (thinking is part of output_tokens, so Total must not grow)", u)
	}
	// a thinking count larger than the output is inconsistent: leave Output alone
	frames = stream(`"end_turn"`, `{"output_tokens":10,"output_tokens_details":{"thinking_tokens":60}}`, "")
	u = last(decode(t, chatstream.DecodeOptions{}, frames, nil, true)).Usage
	if u.Output != 10 || u.Reasoning != 0 {
		t.Errorf("inconsistent thinking count: %+v", u)
	}
}

// The audit found Nanite drops redacted_thinking; it must be captured so it can
// be round-tripped.
func TestRedactedThinkingIsCaptured(t *testing.T) {
	fx, err := conformance.LoadFixture("testdata/redacted_thinking.frames.json")
	if err != nil {
		t.Fatal(err)
	}
	evs, err := conformance.DecodeFixture(anthropic.New(), fx)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := chatstream.Reduce(evs, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := msg.Parts[0]
	if p.Kind != chatstream.PartReasoning || string(p.Meta["redacted"]) != "true" ||
		string(p.Final) != `{"data":"EmwKAhgBEgy3va3pzix/LafPsn4aDFIT2Xlxh0L5L8rLVyIwxtE3rAFBa8cr3qpP"}` {
		t.Errorf("part = %+v", p)
	}
	if msg.Text() != "Done." {
		t.Errorf("text = %q", msg.Text())
	}
}

func TestThinkingSignatureRoundTripsOnPartEnd(t *testing.T) {
	fx, _ := conformance.LoadFixture("testdata/thinking.frames.json")
	evs, _ := conformance.DecodeFixture(anthropic.New(), fx)
	msg, err := chatstream.Reduce(evs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Parts[0].Text != "I need to compare the two options." || !strings.Contains(string(msg.Parts[0].Final), "EqQBCgIYAhIM") {
		t.Errorf("reasoning part = %+v", msg.Parts[0])
	}
}

func TestToolArguments(t *testing.T) {
	fx, _ := conformance.LoadFixture("testdata/tool_use.frames.json")
	evs, _ := conformance.DecodeFixture(anthropic.New(), fx)
	msg, _ := chatstream.Reduce(evs, nil)
	call := msg.Parts[1]
	if call.Kind != chatstream.PartToolCall || string(call.Meta["name"]) != `"get_weather"` || string(call.Meta["id"]) != `"toolu_01T1x1fJ34qAmk2tNTrN7Up6"` {
		t.Errorf("call = %+v", call)
	}
	if got := string(call.Arguments()); got != `{"location":"San Francisco, CA"}` {
		t.Errorf("arguments = %s", got)
	}

	// fine-grained streaming can end mid-value: flagged, not silently "repaired"
	fx, _ = conformance.LoadFixture("testdata/max_tokens_partial_tool.frames.json")
	evs, _ = conformance.DecodeFixture(anthropic.New(), fx)
	var end chatstream.Event
	for _, e := range evs {
		if e.Verb == chatstream.VerbPartEnd {
			end = e
		}
	}
	if len(end.Final) != 0 || string(end.Ext["anthropic"]) != `{"args_invalid":true}` {
		t.Errorf("invalid arguments must not get a Final: %+v", end)
	}

	// a tool call with no arguments at all has empty-object arguments
	evs = decode(t, chatstream.DecodeOptions{}, []fr{
		{"message_start", `{"type":"message_start","message":{"id":"m"}}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"noargs","input":{}}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
	}, nil, true)
	for _, e := range evs {
		if e.Verb == chatstream.VerbPartEnd && string(e.Final) != "{}" {
			t.Errorf("Final = %s", e.Final)
		}
	}
}

// Block index maps to a stable part id, and deltas for two open blocks go to
// their own parts.
func TestPartIDsFollowBlockIndex(t *testing.T) {
	evs := decode(t, chatstream.DecodeOptions{}, []fr{
		{"message_start", `{"type":"message_start","message":{"id":"mm"}}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"B"}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"A"}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"content_block_stop", `{"type":"content_block_stop","index":1}`},
		{"message_stop", `{"type":"message_stop"}`},
	}, nil, true)
	got := map[string]string{}
	for _, e := range evs {
		if e.Verb == chatstream.VerbPartDelta {
			got[e.PartID] += e.Text
		}
	}
	if got["mm:0"] != "A" || got["mm:1"] != "B" || len(got) != 2 {
		t.Errorf("deltas by part = %v", got)
	}
	conformance.Check(t, evs)
}

func TestRunIDComesFromOptionsOrMessageID(t *testing.T) {
	evs := decode(t, chatstream.DecodeOptions{}, stream(`"end_turn"`, `{"output_tokens":1}`, ""), nil, true)
	for _, e := range evs {
		if e.RunID != "m1" {
			t.Fatalf("run id = %q, want the message id", e.RunID)
		}
	}
	evs = decode(t, chatstream.DecodeOptions{RunID: "mine", Provider: "bedrock"}, stream(`"end_turn"`, `{"output_tokens":1}`, ""), nil, true)
	for _, e := range evs {
		if e.RunID != "mine" {
			t.Fatalf("run id = %q, want the option", e.RunID)
		}
	}
	if evs[0].Provider != "bedrock" || evs[0].Model != "mod" {
		t.Errorf("run.start = %+v", evs[0])
	}
}

func TestErrorEventIsTerminalAndCarriesItsType(t *testing.T) {
	retry := map[string]bool{"overloaded_error": true, "api_error": true, "timeout_error": true, "rate_limit_error": true,
		"invalid_request_error": false, "authentication_error": false, "permission_error": false, "not_found_error": false}
	for typ, want := range retry {
		evs := decode(t, chatstream.DecodeOptions{}, []fr{
			{"message_start", `{"type":"message_start","message":{"id":"m"}}`},
			{"error", `{"type":"error","error":{"type":"` + typ + `","message":"x"}}`},
			{"message_stop", `{"type":"message_stop"}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"late"}}`},
		}, nil, true)
		terms := terminals(evs)
		if len(terms) != 1 || terms[0].Verb != chatstream.VerbRunError || terms[0].Code != typ || terms[0].Retryable != want || terms[0].Message != "x" {
			t.Errorf("%s: terminals = %+v", typ, terms)
		}
		if last(evs).Verb != chatstream.VerbRunError {
			t.Errorf("%s: nothing may follow the terminal event: %v", typ, evs[len(evs)-1].Verb)
		}
	}
	// an error with no type still terminates
	evs := decode(t, chatstream.DecodeOptions{}, []fr{{"error", `{"type":"error","error":{}}`}}, nil, true)
	if terms := terminals(evs); len(terms) != 1 || terms[0].Code != chatstream.CodeUpstreamError {
		t.Errorf("typeless error: %+v", evs)
	}
}

// Malformed, unknown and blank frames are never terminal and never dropped
// silently (blank ones carry nothing to drop).
func TestFramePolicy(t *testing.T) {
	d := anthropic.New().NewDecoder(chatstream.DecodeOptions{Now: func() time.Time { return conformance.FixedTime }})
	for _, blank := range []string{"", "  ", "\n"} {
		if evs, err := d.Decode(chatstream.Frame{Data: []byte(blank)}); err != nil || len(evs) != 0 {
			t.Errorf("blank %q: %v %v", blank, evs, err)
		}
	}
	evs, _ := d.Decode(chatstream.Frame{Event: "content_block_delta", Data: []byte("{nope")})
	if len(evs) != 1 || evs[0].Verb != chatstream.VerbRaw || evs[0].Raw.Type != "malformed" || string(evs[0].Raw.Payload) != `"{nope"` || evs[0].IsTerminal() {
		t.Errorf("malformed: %+v", evs)
	}
	if _, err := json.Marshal(evs[0]); err != nil {
		t.Errorf("a raw event for non-JSON data must still marshal: %v", err)
	}
	evs, _ = d.Decode(chatstream.Frame{Event: "brand_new", Data: []byte(`{"type":"brand_new","k":1}`)})
	if len(evs) != 1 || evs[0].Raw.Type != "brand_new" || string(evs[0].Raw.Payload) != `{"type":"brand_new","k":1}` {
		t.Errorf("unknown: %+v", evs)
	}
	// the SSE event name is used when the JSON has no type
	evs, _ = d.Decode(chatstream.Frame{Event: "ping", Data: []byte(`{}`)})
	if len(evs) != 0 {
		t.Errorf("ping by event name: %+v", evs)
	}
	// a stream still works after all that
	evs, _ = d.Decode(chatstream.Frame{Event: "message_start", Data: []byte(`{"type":"message_start","message":{"id":"z"}}`)})
	if len(evs) == 0 || evs[0].Verb != chatstream.VerbRunStart {
		t.Errorf("decoder broken by junk: %+v", evs)
	}
}

// EOF before message_stop was swallowed as success in Nanite. It is an error.
func TestTruncationIsNeverSuccess(t *testing.T) {
	full := stream(`"end_turn"`, `{"output_tokens":9}`, "")
	cases := map[string][]fr{
		"before message_start":       nil,
		"after message_start":        full[:1],
		"mid text block":             full[:3],
		"between blocks":             full[:4],
		"after message_delta":        full[:5], // stop reason already known, message_stop missing
		"mid tool json":              {full[0], {"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"n","input":{}}}`}, {"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}`}},
		"mid thinking, no signature": {full[0], {"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`}, {"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hm"}}`}},
	}
	for name, frames := range cases {
		for _, cause := range []error{nil, io.ErrUnexpectedEOF} {
			evs := decode(t, chatstream.DecodeOptions{}, frames, cause, true)
			terms := terminals(evs)
			if len(terms) != 1 || terms[0].Verb != chatstream.VerbRunError || terms[0].Code != chatstream.CodeUpstreamTruncated || !terms[0].Retryable {
				t.Errorf("%s (cause %v): terminals = %+v", name, cause, terms)
				continue
			}
			conformance.Check(t, evs)
			if cause != nil && !strings.Contains(terms[0].Message, "unexpected EOF") {
				t.Errorf("%s: the cause is missing from %q", name, terms[0].Message)
			}
		}
	}
}

func TestNothingIsEmittedAfterTheTerminalEvent(t *testing.T) {
	frames := append(stream(`"end_turn"`, `{"output_tokens":1}`, ""), stream(`"end_turn"`, `{"output_tokens":1}`, "")...)
	evs := decode(t, chatstream.DecodeOptions{}, frames, nil, true)
	if len(terminals(evs)) != 1 || !last(evs).IsTerminal() {
		t.Fatalf("events after the terminal event: %v", evs)
	}
	conformance.Check(t, evs)
}

func TestCitationBecomesASourcePartInsideTheText(t *testing.T) {
	fx, _ := conformance.LoadFixture("testdata/server_tool.frames.json")
	evs, _ := conformance.DecodeFixture(anthropic.New(), fx)
	msg, err := chatstream.Reduce(evs, nil)
	if err != nil {
		t.Fatal(err)
	}
	var src, text *chatstream.Part
	for i := range msg.Parts {
		switch msg.Parts[i].Kind { //nolint:exhaustive // only these two matter here
		case chatstream.PartSource:
			src = &msg.Parts[i]
		case chatstream.PartText:
			text = &msg.Parts[i]
		}
	}
	if src == nil || text == nil || !strings.Contains(string(src.Final), "web_search_result_location") || text.Text != "Shannon was born in 1916." {
		t.Fatalf("parts = %+v", msg.Parts)
	}
	if msg.Usage.Extra["server_tool_use.web_search_requests"] != 1 {
		t.Errorf("usage extra = %v", msg.Usage.Extra)
	}
}

func TestRefusalKeepsItsDetails(t *testing.T) {
	fx, _ := conformance.LoadFixture("testdata/refusal.frames.json")
	evs, _ := conformance.DecodeFixture(anthropic.New(), fx)
	fin := last(evs)
	if fin.Finish() != chatstream.FinishRefusal || !strings.Contains(string(fin.Ext["anthropic"]), `"category":"cyber"`) {
		t.Errorf("finish = %+v", fin)
	}
}

func TestDecodeAfterCloseIsAnError(t *testing.T) {
	d := anthropic.New().NewDecoder(chatstream.DecodeOptions{})
	d.Close(nil)
	if _, err := d.Decode(chatstream.Frame{Data: []byte(`{}`)}); !errors.Is(err, chatstream.ErrDecoderClosed) {
		t.Fatalf("err = %v", err)
	}
}

// A tool result block names the tool_call part it answers, and says when it
// failed; a result for a call the stream never showed is a data part.
func TestResultCorrelation(t *testing.T) {
	fx, err := conformance.LoadFixture("testdata/result_correlation.frames.json")
	if err != nil {
		t.Fatal(err)
	}
	evs, err := conformance.DecodeFixture(anthropic.New(), fx)
	if err != nil {
		t.Fatal(err)
	}
	conformance.Check(t, evs)
	var call, result, orphan *chatstream.Event
	for i := range evs {
		e := &evs[i]
		if e.Verb != chatstream.VerbPartStart {
			continue
		}
		switch e.PartKind() {
		case chatstream.PartToolCall:
			call = e
		case chatstream.PartToolResult:
			result = e
		case chatstream.PartData:
			orphan = e
		default:
		}
	}
	if call == nil || result == nil || orphan == nil {
		t.Fatalf("parts: %v %v %v", call, result, orphan)
	}
	if call.MetaString(chatstream.MetaName) != "web_search" {
		t.Errorf("call meta = %v", call.Meta)
	}
	if result.MetaString(chatstream.MetaCallID) != call.PartID || !result.MetaBool(chatstream.MetaIsError) {
		t.Errorf("result meta = %v, want call_id %s and is_error (its content is a *_error object)", result.Meta, call.PartID)
	}
	if orphan.MetaString("tool_use_id") != "srv_never_seen" || orphan.MetaString(chatstream.MetaCallID) != "" {
		t.Errorf("orphan meta = %v", orphan.Meta)
	}
	// the well-formed server_tool fixture's result is not an error
	fx, _ = conformance.LoadFixture("testdata/server_tool.frames.json")
	evs, _ = conformance.DecodeFixture(anthropic.New(), fx)
	for _, e := range evs {
		if e.Verb == chatstream.VerbPartStart && e.PartKind() == chatstream.PartToolResult && e.MetaBool(chatstream.MetaIsError) {
			t.Error("a successful search result was flagged is_error")
		}
	}
}

// A second message_start while a message is open ends the open one first, as
// blocks are ended: message.start inside an open message is a lifecycle
// violation.
func TestSecondMessageStartEndsTheOpenMessage(t *testing.T) {
	frames := []fr{
		{"message_start", `{"type":"message_start","message":{"id":"m1","role":"assistant","model":"mod"}}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`},
		{"message_start", `{"type":"message_start","message":{"id":"m2","role":"assistant","model":"mod"}}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}
	evs := decode(t, chatstream.DecodeOptions{}, frames, nil, true)
	conformance.Check(t, evs)
	ends := 0
	for _, e := range evs {
		if e.Verb == chatstream.VerbMessageEnd {
			ends++
		}
	}
	if ends != 2 {
		t.Errorf("message.end events = %d, want 2", ends)
	}
}

func TestTooManyOpenBlocksEndsTheRunNonRetryable(t *testing.T) {
	frames := []fr{{"message_start", `{"type":"message_start","message":{"id":"m1","role":"assistant","model":"mod"}}`}}
	for i := 0; i <= anthropicwire.MaxOpenBlocks; i++ {
		frames = append(frames, fr{"content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, i)})
	}
	evs := decode(t, chatstream.DecodeOptions{}, frames, nil, true)
	fin := last(evs)
	if fin.Verb != chatstream.VerbRunError || fin.Code != chatstream.CodeLimitExceeded || fin.Retryable {
		t.Fatalf("terminal = %+v", fin)
	}
	conformance.Check(t, evs)
}
