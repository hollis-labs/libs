package openaichat_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui-go/chatstream/adapter/openaichat"
	"github.com/hollis-labs/libs/ui-go/chatstream/conformance"
)

func chunk(delta string, finish string) string {
	fr := "null"
	if finish != "" {
		fr = fmt.Sprintf("%q", finish)
	}
	return fmt.Sprintf(`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":%s,"finish_reason":%s}]}`, delta, fr)
}

func usageChunk(u string) string {
	return fmt.Sprintf(`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"usage":%s}`, u)
}

// run decodes datas (each one frame) and closes the decoder with a clean EOF.
func run(t *testing.T, datas ...string) []chatstream.Event {
	t.Helper()
	d := openaichat.New().NewDecoder(conformance.DecodeOptions())
	var out []chatstream.Event
	for _, s := range datas {
		evs, err := d.Decode(chatstream.Frame{Data: []byte(s)})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, evs...)
	}
	return append(out, d.Close(nil)...)
}

func last(evs []chatstream.Event) chatstream.Event { return evs[len(evs)-1] }

func verbsOf(evs []chatstream.Event) string {
	var v []string
	for _, e := range evs {
		v = append(v, string(e.Verb))
	}
	return strings.Join(v, " ")
}

// Every documented finish_reason maps to the closed vocabulary and keeps its
// upstream word; anything else is "other".
func TestFinishReasonTable(t *testing.T) {
	table := map[string]chatstream.FinishReason{
		"stop":           chatstream.FinishStop,
		"length":         chatstream.FinishLength,
		"content_filter": chatstream.FinishContentFilter,
		"tool_calls":     chatstream.FinishToolCalls,
		"function_call":  chatstream.FinishToolCalls,
		"end_turn":       chatstream.FinishOther, // not this dialect's word
		"":               chatstream.FinishOther,
	}
	for raw, want := range table {
		frames := []string{chunk(`{"content":"x"}`, "")}
		if raw != "" {
			frames = append(frames, chunk(`{}`, raw))
		}
		frames = append(frames, "[DONE]")
		fin := last(run(t, frames...))
		if fin.Verb != chatstream.VerbRunFinish || fin.Finish() != want || fin.RawReason != raw {
			t.Errorf("finish_reason %q: %s reason %q raw %q, want %q", raw, fin.Verb, fin.Reason, fin.RawReason, want)
		}
	}
}

// The usage double count: prompt_tokens already includes cached tokens, so the
// components must add up to prompt+completion, not prompt+cached+completion.
func TestUsageIsDisjointNotDoubleCounted(t *testing.T) {
	evs := run(t, chunk(`{"content":"x"}`, "stop"),
		usageChunk(`{"prompt_tokens":1000,"completion_tokens":200,"total_tokens":1200,"prompt_tokens_details":{"cached_tokens":600,"cache_write_tokens":100},"completion_tokens_details":{"reasoning_tokens":50}}`),
		"[DONE]")
	u := last(evs).Usage
	if u == nil || u.Scope != chatstream.UsageFinal {
		t.Fatalf("usage = %+v", u)
	}
	if u.UncachedInput != 300 || u.CacheRead != 600 || u.CacheWrite != 100 || u.Output != 150 || u.Reasoning != 50 {
		t.Errorf("components = %+v", u)
	}
	if u.Total() != 1200 {
		t.Errorf("Total = %d, want prompt+completion = 1200", u.Total())
	}
	if naive := 1000 + 600 + 100 + 200; naive == u.Total() {
		t.Error("vacuous: naive sum equals Total")
	}
	if got := usageEvents(evs); got != 0 {
		t.Errorf("usage on run.finish must not also appear as a usage event (%d)", got)
	}
}

func usageEvents(evs []chatstream.Event) int {
	n := 0
	for _, e := range evs {
		if e.Verb == chatstream.VerbUsage {
			n++
		}
	}
	return n
}

func TestUsageOnAFinishChunkAndPredictionCountersInExtra(t *testing.T) {
	// some servers put usage on the finish_reason chunk itself
	c := `{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"accepted_prediction_tokens":2,"rejected_prediction_tokens":1,"audio_tokens":4}}}`
	u := last(run(t, chunk(`{"content":"x"}`, ""), c, "[DONE]")).Usage
	if u == nil || u.Total() != 15 || u.Extra["accepted_prediction_tokens"] != 2 || u.Extra["rejected_prediction_tokens"] != 1 || u.Extra["completion_audio_tokens"] != 4 {
		t.Fatalf("usage = %+v", u)
	}
}

// Truncation: the audit found "OpenAI Chat without [DONE]" swallowed. Whatever
// the stream had reached, the end without [DONE] is a retryable error.
func TestTruncationCases(t *testing.T) {
	tests := []struct {
		name   string
		frames []string
		seen   string // finish_reason_seen
		usage  bool
	}{
		{"no frames", nil, "", false},
		{"mid text", []string{chunk(`{"content":"par"}`, "")}, "", false},
		{"mid tool arguments", []string{chunk(`{"tool_calls":[{"index":0,"id":"a","function":{"name":"f","arguments":"{\"x\":"}}]}`, "")}, "", false},
		{"after finish_reason, before [DONE]", []string{chunk(`{"content":"x"}`, ""), chunk(`{}`, "stop")}, "stop", false},
		{"after usage chunk, before [DONE]", []string{chunk(`{"content":"x"}`, ""), chunk(`{}`, "stop"),
			usageChunk(`{"prompt_tokens":3,"completion_tokens":4}`)}, "stop", true},
	}
	for _, tc := range tests {
		evs := run(t, tc.frames...)
		fin := last(evs)
		if fin.Verb != chatstream.VerbRunError || fin.Code != chatstream.CodeUpstreamTruncated || !fin.Retryable {
			t.Errorf("%s: last = %s %q retryable=%v; truncation must be a retryable error, never a finish", tc.name, fin.Verb, fin.Code, fin.Retryable)
		}
		if tc.seen != "" {
			var ext struct {
				Seen string `json:"finish_reason_seen"`
			}
			_ = json.Unmarshal(fin.Ext["openai"], &ext)
			if ext.Seen != tc.seen {
				t.Errorf("%s: finish_reason_seen = %q, want %q", tc.name, ext.Seen, tc.seen)
			}
		}
		if got := usageEvents(evs) == 1; got != tc.usage {
			t.Errorf("%s: usage event present = %v, want %v (a usage report received before truncation must not be lost)", tc.name, got, tc.usage)
		}
		conformance.Check(t, evs)
		if err := conformance.CheckReplayEquivalence(evs); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

func TestDoneIsTheTerminalSignal(t *testing.T) {
	evs := run(t, chunk(`{"content":"x"}`, "stop"), "[DONE]")
	if fin := last(evs); fin.Verb != chatstream.VerbRunFinish {
		t.Fatalf("last = %s", fin.Verb)
	}
	for _, e := range evs[:len(evs)-1] {
		if e.IsTerminal() {
			t.Fatalf("finish_reason alone must not be terminal: %s", verbsOf(evs))
		}
	}
	// frames after [DONE] are ignored
	d := openaichat.New().NewDecoder(conformance.DecodeOptions())
	_, _ = d.Decode(chatstream.Frame{Data: []byte(chunk(`{"content":"x"}`, "stop"))})
	_, _ = d.Decode(chatstream.Frame{Data: []byte("[DONE]")})
	late, err := d.Decode(chatstream.Frame{Data: []byte(chunk(`{"content":"late"}`, ""))})
	if err != nil || len(late) != 0 {
		t.Fatalf("events after the terminal event: %v %v", late, err)
	}
}

func TestEmptyDataAndWhitespaceAreIgnored(t *testing.T) {
	evs := run(t, "", "  \n", chunk(`{"content":"x"}`, "stop"), "[DONE]")
	if strings.Count(verbsOf(evs), "raw") != 0 {
		t.Fatalf("empty frames must not produce raw events: %s", verbsOf(evs))
	}
	if fin := last(evs); fin.Verb != chatstream.VerbRunFinish {
		t.Fatalf("last = %s", fin.Verb)
	}
	// [DONE] with surrounding whitespace is still [DONE]
	if fin := last(run(t, chunk(`{"content":"x"}`, "stop"), " [DONE] \r\n")); fin.Verb != chatstream.VerbRunFinish {
		t.Fatalf("last = %s", fin.Verb)
	}
}

func TestErrorFrameClassification(t *testing.T) {
	tests := []struct {
		body      string
		code      string
		retryable bool
	}{
		{`{"error":{"message":"slow down","type":"rate_limit_exceeded","code":"rate_limit_exceeded"}}`, "rate_limit_exceeded", true},
		{`{"error":{"message":"oops","type":"server_error","code":null}}`, "server_error", true},
		{`{"error":{"message":"overloaded","type":"overloaded_error"}}`, "overloaded_error", true},
		{`{"error":{"message":"too many","code":429}}`, "429", true},
		{`{"error":{"message":"bad key","type":"invalid_request_error","code":"invalid_api_key"}}`, "invalid_api_key", false},
		{`{"error":{"message":"quota","type":"insufficient_quota","code":"insufficient_quota"}}`, "insufficient_quota", false},
		{`{"error":{"message":"no code or type"}}`, chatstream.CodeUpstreamError, false},
		{`{"error":"plain string error"}`, chatstream.CodeUpstreamError, false},
	}
	for _, tc := range tests {
		evs := run(t, chunk(`{"content":"x"}`, ""), tc.body)
		fin := last(evs)
		if fin.Verb != chatstream.VerbRunError || fin.Code != tc.code || fin.Retryable != tc.retryable || fin.Message == "" {
			t.Errorf("%s: %s code %q retryable %v msg %q", tc.body, fin.Verb, fin.Code, fin.Retryable, fin.Message)
		}
		if fin.Raw == nil || string(fin.Raw.Payload) != tc.body {
			t.Errorf("%s: the upstream frame must be preserved on the event: %+v", tc.body, fin.Raw)
		}
		conformance.Check(t, evs)
		// after an error nothing more is emitted, and Close adds nothing
		if strings.Count(verbsOf(evs), "run.error") != 1 {
			t.Errorf("%s: %s", tc.body, verbsOf(evs))
		}
	}
	// "error": null is not an error
	if fin := last(run(t, `{"id":"c1","error":null,"choices":[]}`, "[DONE]")); fin.Verb != chatstream.VerbRunFinish {
		t.Errorf("null error treated as failure: %s", fin.Verb)
	}
}

// One part per tool-call index, keyed by the index, however fragments interleave.
func TestToolCallIndexMapsToPartID(t *testing.T) {
	tc := func(idx int, id, name, args string) string {
		return chunk(fmt.Sprintf(`{"tool_calls":[{"index":%d,"id":%q,"function":{"name":%q,"arguments":%q}}]}`, idx, id, name, args), "")
	}
	evs := run(t,
		tc(1, "b", "second", `{"y":`),
		tc(0, "a", "first", `{"x":`),
		chunk(`{"tool_calls":[{"index":1,"function":{"arguments":"2}"}}]}`, ""),
		chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}`, ""),
		chunk(`{}`, "tool_calls"), "[DONE]")
	final := map[string]string{}
	var deltas = map[string]string{}
	for _, e := range evs {
		switch e.Verb {
		case chatstream.VerbPartEnd:
			final[e.PartID] = string(e.Final)
		case chatstream.VerbPartDelta:
			deltas[e.PartID] += e.JSONFragment
		default:
		}
	}
	if final["a"] != `{"x":1}` || final["b"] != `{"y":2}` || deltas["a"] != `{"x":1}` || deltas["b"] != `{"y":2}` {
		t.Fatalf("finals %v deltas %v: fragments went to the wrong part", final, deltas)
	}
	// parts end in index order, deterministically
	var ends []string
	for _, e := range evs {
		if e.Verb == chatstream.VerbPartEnd {
			ends = append(ends, e.PartID)
		}
	}
	if strings.Join(ends, ",") != "b,a" {
		t.Errorf("part.end order = %v, want the order the calls began", ends)
	}
	m, err := chatstream.Reduce(evs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(m.Parts[0].Arguments()); got != `{"y":2}` && got != `{"x":1}` {
		t.Errorf("arguments = %s", got)
	}
}

func TestTextThenToolClosesTheTextPart(t *testing.T) {
	evs := run(t, chunk(`{"content":"go"}`, ""),
		chunk(`{"tool_calls":[{"index":0,"id":"a","function":{"name":"f","arguments":"{}"}}]}`, ""),
		chunk(`{}`, "tool_calls"), "[DONE]")
	got := verbsOf(evs)
	if !strings.Contains(got, "part.delta part.end part.start") {
		t.Errorf("the text part must close before the tool call opens: %s", got)
	}
	conformance.Check(t, evs)
}

func TestNonObjectChunkFieldsArePreservedNotTerminal(t *testing.T) {
	evs := run(t, chunk(`{"content":"a"}`, ""), `{"id":"c1","choices":"nope"}`, `{"id":"c1","choices":[{"index":0,"delta":"str"}]}`,
		chunk(`{"content":"b"}`, "stop"), "[DONE]")
	if strings.Count(verbsOf(evs), "raw") != 2 {
		t.Errorf("verbs: %s", verbsOf(evs))
	}
	if last(evs).Verb != chatstream.VerbRunFinish {
		t.Errorf("a malformed frame must not be terminal: %s", verbsOf(evs))
	}
	m, err := chatstream.Reduce(evs, nil)
	if err != nil || m.Text() != "ab" {
		t.Errorf("text %q err %v", m.Text(), err)
	}
}

func TestRunIDProviderAndModelDefaults(t *testing.T) {
	d := openaichat.New().NewDecoder(chatstream.DecodeOptions{Provider: "openrouter", Model: "fallback"})
	evs, _ := d.Decode(chatstream.Frame{Data: []byte(`{"id":"chatcmpl-9","choices":[{"index":0,"delta":{"content":"x"}}]}`)})
	if evs[0].Verb != chatstream.VerbRunStart || evs[0].RunID != "chatcmpl-9" || evs[0].Provider != "openrouter" || evs[0].Model != "fallback" {
		t.Errorf("run.start = %+v", evs[0])
	}
	for _, e := range evs {
		if e.RunID != "chatcmpl-9" || e.Time.IsZero() || e.V != chatstream.SchemaVersion {
			t.Errorf("unstamped event %+v", e)
		}
	}
	if evs[1].MessageID != "chatcmpl-9" || evs[1].Role != "assistant" {
		t.Errorf("message.start = %+v", evs[1])
	}
}

func TestDecodeAfterCloseIsAnError(t *testing.T) {
	d := openaichat.New().NewDecoder(conformance.DecodeOptions())
	d.Close(nil)
	if _, err := d.Decode(chatstream.Frame{Data: []byte("[DONE]")}); !errors.Is(err, chatstream.ErrDecoderClosed) {
		t.Fatalf("err = %v", err)
	}
}

func toolNames(evs []chatstream.Event) map[string]string {
	out := map[string]string{}
	for _, e := range evs {
		if e.Verb == chatstream.VerbPartStart && e.Kind == string(chatstream.PartToolCall) {
			out[e.PartID] = e.MetaString(chatstream.MetaName)
		}
	}
	return out
}

// part.start of a tool_call must carry the tool's name. The reference puts it on
// the first fragment; a server that sends it later has its arguments held until
// it arrives, and a call that never gets one is opened as "unknown".
func TestToolCallPartsAlwaysCarryAName(t *testing.T) {
	first := run(t, chunk(`{"tool_calls":[{"index":0,"id":"a","function":{"name":"get","arguments":"{}"}}]}`, "tool_calls"), "[DONE]")
	if got := toolNames(first); got["a"] != "get" {
		t.Errorf("names = %v", got)
	}

	late := run(t,
		chunk(`{"tool_calls":[{"index":0,"id":"a","function":{"arguments":"{\"x\":"}}]}`, ""),
		chunk(`{"tool_calls":[{"index":0,"function":{"name":"late","arguments":"1}"}}]}`, ""),
		chunk(`{}`, "tool_calls"), "[DONE]")
	if got := toolNames(late); got["a"] != "late" {
		t.Errorf("late name: %v", got)
	}
	var frags []string
	started := false
	for _, e := range late {
		if e.Verb == chatstream.VerbPartStart {
			started = true
		}
		if e.Verb == chatstream.VerbPartDelta {
			if !started {
				t.Error("an argument fragment came before part.start")
			}
			frags = append(frags, e.JSONFragment)
		}
	}
	if strings.Join(frags, "") != `{"x":1}` {
		t.Errorf("held fragments were lost or reordered: %q", frags)
	}
	conformance.Check(t, late)

	never := run(t, chunk(`{"tool_calls":[{"index":0,"id":"a","function":{"arguments":"{}"}}]}`, "tool_calls"), "[DONE]")
	if got := toolNames(never); got["a"] != "unknown" {
		t.Errorf("never named: %v", got)
	}
	for _, e := range never {
		if e.Verb == chatstream.VerbPartStart && !strings.Contains(string(e.Ext["openai"]), "name_missing") {
			t.Errorf("a placeholder name must be flagged: %+v", e.Ext)
		}
	}
	conformance.Check(t, never)

	// truncated while still waiting for the name: the held call is opened, not lost
	cut := run(t, chunk(`{"tool_calls":[{"index":0,"id":"a","function":{"arguments":"{\"y\":2}"}}]}`, ""))
	if got := toolNames(cut); got["a"] != "unknown" {
		t.Errorf("truncated before the name: %v", got)
	}
	conformance.Check(t, cut)
}

// Some servers send finish_reason "" on every chunk. That is not a finish: it
// must not close the open part or count as the stream having reached its end.
func TestEmptyStringFinishReasonIsNotAFinish(t *testing.T) {
	empty := func(delta string) string {
		return fmt.Sprintf(`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":%s,"finish_reason":""}]}`, delta)
	}
	evs := run(t, empty(`{"role":"assistant","content":"a"}`), empty(`{"content":"b"}`), chunk(`{}`, "stop"), "[DONE]")
	starts := 0
	for _, e := range evs {
		if e.Verb == chatstream.VerbPartStart && e.Kind == "text" {
			starts++
		}
	}
	if starts != 1 {
		t.Errorf("text parts = %d, want 1: an empty finish_reason split the text: %s", starts, verbsOf(evs))
	}
	if fin := last(evs); fin.Verb != chatstream.VerbRunFinish || fin.Finish() != chatstream.FinishStop || fin.RawReason != "stop" {
		t.Errorf("terminal = %+v", fin)
	}
	conformance.Check(t, evs)

	// and a stream cut after only empty finish_reasons has not reached a finish
	cut := run(t, empty(`{"content":"a"}`))
	end := last(cut)
	if end.Verb != chatstream.VerbRunError || end.Code != chatstream.CodeUpstreamTruncated {
		t.Fatalf("terminal = %+v", end)
	}
	if _, seen := end.Ext["openai"]; seen {
		t.Errorf("finish_reason_seen was set for an empty finish_reason: %s", end.Ext["openai"])
	}
}

// The error frame is terminal: a mistyped field must not lose its message or its
// classification.
func TestErrorFrameWithLooselyTypedFields(t *testing.T) {
	evs := run(t, chunk(`{"content":"x"}`, ""), `{"error":{"message":{"detail":"slow down"},"type":"server_error","code":500,"param":3}}`)
	fin := last(evs)
	if fin.Verb != chatstream.VerbRunError || fin.Code != "500" || !fin.Retryable || fin.Message != `{"detail":"slow down"}` {
		t.Errorf("terminal = %+v", fin)
	}
	conformance.Check(t, evs)
}

func TestTooManyToolCallIndicesEndsTheRunNonRetryable(t *testing.T) {
	var frames []string
	for i := 0; i <= openaichat.MaxToolCalls; i++ {
		frames = append(frames, chunk(fmt.Sprintf(`{"tool_calls":[{"index":%d,"id":"c%d","function":{"name":"f","arguments":"{"}}]}`, i, i), ""))
	}
	evs := run(t, frames...)
	fin := last(evs)
	if fin.Verb != chatstream.VerbRunError || fin.Code != chatstream.CodeLimitExceeded || fin.Retryable {
		t.Fatalf("terminal = %+v", fin)
	}
	conformance.Check(t, evs)
}
