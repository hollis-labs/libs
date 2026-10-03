package openaicompat_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/sink/openaicompat"
	"github.com/hollis-labs/go-chatstream/sink/sinktest"
)

func chunks(t *testing.T, evs []chatstream.Event) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, f := range sinktest.Play(t, newEnc(), sinktest.Scenario{Name: "custom", Events: evs}) {
		if f.Data == "[DONE]" {
			out = append(out, map[string]any{"done": true})
			continue
		}
		out = append(out, sinktest.MustJSON(t, f))
	}
	return out
}

func named(t *testing.T, name string) []map[string]any {
	return chunks(t, sinktest.Named(t, name).Events)
}

func delta(c map[string]any) map[string]any {
	ch, _ := c["choices"].([]any)
	if len(ch) == 0 {
		return nil
	}
	d, _ := ch[0].(map[string]any)["delta"].(map[string]any)
	return d
}

func finish(c map[string]any) any {
	ch, _ := c["choices"].([]any)
	if len(ch) == 0 {
		return nil
	}
	return ch[0].(map[string]any)["finish_reason"]
}

func TestFinishReasonTable(t *testing.T) {
	want := map[chatstream.FinishReason]string{
		chatstream.FinishStop: "stop", chatstream.FinishLength: "length", chatstream.FinishToolCalls: "tool_calls",
		chatstream.FinishRefusal: "stop", chatstream.FinishContentFilter: "content_filter", chatstream.FinishPause: "stop",
		chatstream.FinishContextExceeded: "length", chatstream.FinishTurnLimit: "length", chatstream.FinishCancelled: "stop",
		chatstream.FinishOther: "stop", chatstream.FinishError: "", "never-heard-of-it": "stop",
	}
	for in, out := range want {
		if got := openaicompat.FinishReason(in); got != out {
			t.Errorf("FinishReason(%q) = %q, want %q", in, got, out)
		}
	}
}

func TestEveryFinishReasonEndsTheStreamWithItsMapping(t *testing.T) {
	for r, want := range map[chatstream.FinishReason]string{
		chatstream.FinishStop: "stop", chatstream.FinishLength: "length", chatstream.FinishToolCalls: "tool_calls",
		chatstream.FinishContentFilter: "content_filter", chatstream.FinishContextExceeded: "length",
	} {
		cs := chunks(t, sinktest.Build(func(b *sinktest.Builder) { b.Start(); b.Finish(r, "", nil) }))
		if got := finish(cs[len(cs)-2]); got != want {
			t.Errorf("%s: finish_reason = %v, want %s", r, got, want)
		}
	}
	// FinishError has no finish_reason: an error frame instead
	cs := chunks(t, sinktest.Build(func(b *sinktest.Builder) { b.Start(); b.Finish(chatstream.FinishError, "", nil) }))
	e, ok := cs[len(cs)-2]["error"].(map[string]any)
	if !ok || e["code"] != "error" {
		t.Errorf("error finish = %v", cs[len(cs)-2])
	}
}

func TestRoleChunkFirstAndShape(t *testing.T) {
	cs := named(t, "plain_text")
	d := delta(cs[0])
	if d["role"] != "assistant" || d["content"] != "" {
		t.Errorf("first chunk delta = %v", d)
	}
	for _, c := range cs[:len(cs)-1] {
		if c["object"] != "chat.completion.chunk" || c["id"] != "chatcmpl-run-1" || c["model"] != "claude-x" || c["created"] != float64(sinktest.T0.Unix()) {
			t.Errorf("chunk envelope = %v", c)
		}
	}
	if cs[len(cs)-1]["done"] != true {
		t.Error("[DONE] last")
	}
}

func TestToolCallsCarryIncreasingIndexes(t *testing.T) {
	cs := chunks(t, sinktest.Build(func(b *sinktest.Builder) {
		b.Start()
		b.Part("a", chatstream.PartToolCall, sinktest.Meta("name", "one"))
		b.Frag("a", `{"x":`)
		b.Frag("a", `1}`)
		b.End("a", "")
		b.Part("b", chatstream.PartToolCall, sinktest.Meta("name", "two"))
		b.End("b", `{"y":2}`)
		b.Finish(chatstream.FinishToolCalls, "", nil)
	}))
	var got []string
	for _, c := range cs {
		d := delta(c)
		if d == nil {
			continue
		}
		if tcs, ok := d["tool_calls"].([]any); ok {
			tc := tcs[0].(map[string]any)
			fn := tc["function"].(map[string]any)
			got = append(got, strings.Join([]string{
				toString(tc["index"]), toString(tc["id"]), toString(fn["name"]), toString(fn["arguments"])}, "|"))
		}
	}
	want := []string{`0|a|one|`, `0|||{"x":`, `0|||1}`, `1|b|two|`, `1|||{"y":2}`}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("tool_calls chunks:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func TestUsageChunkIsInclusiveAndConvertsBack(t *testing.T) {
	cs := named(t, "usage_with_cache_and_reasoning")
	u := cs[len(cs)-2]
	if len(u["choices"].([]any)) != 0 {
		t.Errorf("the usage chunk has empty choices: %v", u)
	}
	us := u["usage"].(map[string]any)
	if us["prompt_tokens"] != float64(1050) || us["completion_tokens"] != float64(200) || us["total_tokens"] != float64(1250) {
		t.Errorf("usage = %v", us)
	}
	if us["prompt_tokens_details"].(map[string]any)["cached_tokens"] != float64(600) ||
		us["completion_tokens_details"].(map[string]any)["reasoning_tokens"] != float64(50) {
		t.Errorf("details = %v", us)
	}
	// the conversion is exact both ways: the inclusive counts rebuild the same components
	in := chatstream.Usage{UncachedInput: 400, CacheRead: 600, CacheWrite: 50, Output: 150, Reasoning: 50}
	m := openaicompat.UsageJSON(in)
	back, err := chatstream.UsageFromInclusive(chatstream.UsageFinal, m["prompt_tokens"].(int), 600, 50, m["completion_tokens"].(int), 50)
	if err != nil || back.UncachedInput != in.UncachedInput || back.Output != in.Output || back.Total() != in.Total() {
		t.Errorf("round trip = %+v, %v", back, err)
	}
	// no usage reported: no usage chunk
	plain := named(t, "plain_text")
	for _, c := range plain {
		if _, has := c["usage"]; has {
			t.Errorf("unexpected usage chunk: %v", c)
		}
	}
}

func TestErrorAndAbortFrames(t *testing.T) {
	cs := named(t, "error_terminal")
	e := cs[len(cs)-2]["error"].(map[string]any)
	if e["message"] != "slow down" || e["code"] != "rate_limited" || e["type"] != "server_error" {
		t.Errorf("error = %v", e)
	}
	cs = named(t, "abort_terminal")
	e = cs[len(cs)-2]["error"].(map[string]any)
	if e["type"] != "aborted" || e["code"] != "run_aborted" || !strings.Contains(e["message"].(string), "user canceled") {
		t.Errorf("abort = %v", e)
	}
	for _, name := range []string{"error_terminal", "abort_terminal"} {
		for _, c := range named(t, name) {
			if finish(c) != nil {
				t.Errorf("%s: a finish_reason chunk after an error terminal: %v", name, c)
			}
		}
	}
}

func TestReasoningRefusalAndDroppedParts(t *testing.T) {
	cs := named(t, "reasoning_then_text")
	if delta(cs[1])["reasoning_content"] != "let me think " {
		t.Errorf("reasoning chunk = %v", cs[1])
	}
	cs = named(t, "source_file_data_parts")
	if delta(cs[1])["refusal"] != "I can't help with that." || len(cs) != 4 {
		t.Errorf("chunks = %v", cs)
	}
	// tool results, approvals, raw and activity never reach the client
	for _, name := range []string{"tool_call_and_result", "approval_inband_after_call", "raw_and_activity"} {
		for _, c := range named(t, name) {
			if strings.Contains(sinktestJSON(c), "sunny") || strings.Contains(sinktestJSON(c), "ap-1") {
				t.Errorf("%s leaked: %v", name, c)
			}
		}
	}
}

func TestGapBecomesAnSSEComment(t *testing.T) {
	rec := &sinktest.Recorder{}
	enc := newEnc()
	for _, ev := range sinktest.Named(t, "gap_in_stream").Events {
		if err := enc.Encode(rec, ev); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(rec.String(), ": gap 5..9 (retention)\n") {
		t.Errorf("no gap comment in:\n%s", rec.String())
	}
}

func TestTextSurvivesFramingIntact(t *testing.T) {
	frames := sinktest.Play(t, newEnc(), sinktest.Named(t, "escaping_and_unicode"))
	var b strings.Builder
	for _, f := range frames {
		if f.Data == "[DONE]" {
			continue
		}
		if d := delta(sinktest.MustJSON(t, f)); d != nil {
			if s, ok := d["content"].(string); ok {
				b.WriteString(s)
			}
		}
	}
	if b.String() != sinktest.EscapingText() {
		t.Errorf("text = %q", b.String())
	}
}

func TestHeaders(t *testing.T) {
	if openaicompat.New().Headers().Get("Content-Type") != "text/event-stream" || openaicompat.New().Name() != "openaicompat" {
		t.Error("identity")
	}
}

func sinktestJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestOutOfOrderEventsAreErrorsNotSilence(t *testing.T) {
	sinktest.OutOfOrderErrors(t, newEnc)
}
