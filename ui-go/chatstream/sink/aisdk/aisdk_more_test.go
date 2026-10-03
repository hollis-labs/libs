package aisdk_test

import (
	"encoding/json"
	"strings"
	"testing"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/sink/aisdk"
	"github.com/hollis-labs/go-chatstream/sink/sinktest"
)

func chunks(t *testing.T, name string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, f := range sinktest.Play(t, newEnc(), sinktest.Named(t, name)) {
		if f.Data == "[DONE]" {
			out = append(out, map[string]any{"type": "[DONE]"})
			continue
		}
		out = append(out, sinktest.MustJSON(t, f))
	}
	return out
}

func types(cs []map[string]any) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c["type"].(string))
	}
	return out
}

func count(cs []map[string]any, typ string) int {
	n := 0
	for _, c := range cs {
		if c["type"] == typ {
			n++
		}
	}
	return n
}

func TestHeaders(t *testing.T) {
	h := aisdk.New().Headers()
	if h.Get("x-vercel-ai-ui-message-stream") != "v1" || h.Get("Content-Type") != "text/event-stream" {
		t.Errorf("headers = %v", h)
	}
	// SSE data frames only: no event or id lines, as the package's own writer
	for _, sc := range sinktest.Scenarios() {
		for _, f := range sinktest.Play(t, newEnc(), sc) {
			if f.Event != "" || f.ID != "" {
				t.Fatalf("%s: frame has event %q id %q; the AI SDK stream is data lines only", sc.Name, f.Event, f.ID)
			}
		}
	}
}

// The stream is a start, steps, then exactly one of finish / error / abort, and
// [DONE] last. Nothing follows the terminal chunk except [DONE].
func TestEveryScenarioEndsWithOneTerminalChunkThenDone(t *testing.T) {
	for _, sc := range sinktest.Scenarios() {
		cs := chunks(t, sc.Name)
		ts := types(cs)
		if ts[len(ts)-1] != "[DONE]" {
			t.Errorf("%s: last chunk %s", sc.Name, ts[len(ts)-1])
		}
		term := 0
		for i, ty := range ts {
			if ty == "finish" || ty == "error" || ty == "abort" {
				term++
				if i != len(ts)-2 {
					t.Errorf("%s: terminal chunk %s at %d of %d", sc.Name, ty, i, len(ts))
				}
			}
		}
		if term != 1 {
			t.Errorf("%s: %d terminal chunks in %v", sc.Name, term, ts)
		}
		// every text/reasoning block and step is closed before the terminal chunk
		open := map[string]int{}
		for _, c := range cs {
			ty := c["type"].(string)
			switch {
			case strings.HasSuffix(ty, "-start") && (strings.HasPrefix(ty, "text") || strings.HasPrefix(ty, "reasoning")):
				open[c["id"].(string)]++
			case strings.HasSuffix(ty, "-end") && (strings.HasPrefix(ty, "text") || strings.HasPrefix(ty, "reasoning")):
				open[c["id"].(string)]--
			case ty == "start-step":
				open["step"]++
			case ty == "finish-step":
				open["step"]--
			}
		}
		for id, n := range open {
			if n != 0 {
				t.Errorf("%s: %s left unbalanced (%d)", sc.Name, id, n)
			}
		}
	}
}

// The three orderings of approval and call converge on one tool part: one
// tool-input-available, one approval request, no input delta after the input
// is available, and the same toolCallId throughout.
func TestApprovalConvergesOnOneToolPartInAnyOrder(t *testing.T) {
	for _, name := range []string{"approval_inband_after_call", "approval_inband_before_call", "approval_inband_no_call_id"} {
		cs := chunks(t, name)
		if got := count(cs, "tool-input-available"); got != 1 {
			t.Errorf("%s: %d tool-input-available in %v", name, got, types(cs))
		}
		if got := count(cs, "tool-approval-request"); got != 1 {
			t.Errorf("%s: %d approval requests", name, got)
		}
		ids := map[string]bool{}
		availAt, deltaAfter := -1, false
		for i, c := range cs {
			if id, ok := c["toolCallId"].(string); ok {
				ids[id] = true
			}
			switch c["type"] {
			case "tool-input-available":
				availAt = i
			case "tool-input-delta":
				if availAt >= 0 {
					deltaAfter = true
				}
			}
		}
		if len(ids) != 1 {
			t.Errorf("%s: %d distinct toolCallIds %v; the call and its approval are one part", name, len(ids), ids)
		}
		if deltaAfter {
			t.Errorf("%s: an input delta after the input was available", name)
		}
	}
	cs := chunks(t, "approval_inband_no_call_id")
	if cs[2]["toolCallId"] != "approval-ap-9" {
		t.Errorf("an approval with no call id creates the part as approval-<id>: %v", cs[2])
	}
}

func TestApprovalWithoutCallIDBindsToTheNewestUnfinishedCallOfTheSameTool(t *testing.T) {
	sc := sinktest.Named(t, "plain_text")
	evs := scenario(t, func(b *sinktest.Builder) {
		b.Start()
		b.Part("c1", chatstream.PartToolCall, sinktest.Meta("name", "bash"))
		b.End("c1", `{"cmd":"a"}`)
		b.Part("c2", chatstream.PartToolCall, sinktest.Meta("name", "bash"))
		b.End("c2", `{"cmd":"b"}`)
		b.Approval("ap", "", chatstream.ApprovalInBand, `{"tool":"bash"}`)
		b.Finish(chatstream.FinishToolCalls, "", nil)
	})
	_ = sc
	cs := playEvents(t, evs)
	var req map[string]any
	for _, c := range cs {
		if c["type"] == "tool-approval-request" {
			req = c
		}
	}
	if req == nil || req["toolCallId"] != "c2" {
		t.Fatalf("approval request = %v; want it on the newest bash call c2", req)
	}
	if count(cs, "tool-input-available") != 2 {
		t.Errorf("no extra tool part should be created: %v", types(cs))
	}
}

func TestReplaceContentIsResetStepThenTheReplacement(t *testing.T) {
	cs := chunks(t, "raw_and_activity")
	ts := types(cs)
	i := indexOf(ts, "reset-step")
	if i < 0 || ts[i+1] != "text-start" || ts[i+2] != "text-delta" || ts[i+3] != "text-end" {
		t.Fatalf("chunks = %v", ts)
	}
	if cs[i+2]["delta"] != "replaced" {
		t.Errorf("replacement delta = %v", cs[i+2])
	}
}

func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}

// After a reset-step, a tool result for a call from the discarded step would
// make the client throw, so the part is recreated first.
func TestToolResultForAForgottenCallRecreatesThePart(t *testing.T) {
	cs := playEvents(t, scenario(t, func(b *sinktest.Builder) {
		b.Start()
		b.Part("c1", chatstream.PartToolCall, sinktest.Meta("name", "bash"))
		b.End("c1", `{}`)
		b.Activity(sinkReplace, `{"content":""}`)
		b.Part("r1", chatstream.PartToolResult, sinktest.Meta("call_id", "c1", "name", "bash"))
		b.Text("r1", "late")
		b.End("r1", "")
		b.Finish(chatstream.FinishStop, "", nil)
	}))
	var seq []string
	for _, c := range cs {
		switch c["type"] {
		case "tool-input-available", "reset-step", "tool-output-available":
			seq = append(seq, c["type"].(string))
		}
	}
	want := "tool-input-available reset-step tool-input-available tool-output-available"
	if strings.Join(seq, " ") != want {
		t.Fatalf("sequence = %v, want %s", seq, want)
	}
}

func TestFinishReasonTable(t *testing.T) {
	want := map[chatstream.FinishReason]string{
		chatstream.FinishStop: "stop", chatstream.FinishLength: "length", chatstream.FinishToolCalls: "tool-calls",
		chatstream.FinishRefusal: "content-filter", chatstream.FinishContentFilter: "content-filter",
		chatstream.FinishPause: "other", chatstream.FinishContextExceeded: "other", chatstream.FinishTurnLimit: "other",
		chatstream.FinishCancelled: "other", chatstream.FinishError: "error", chatstream.FinishOther: "other",
		"never-heard-of-it": "other",
	}
	for in, out := range want {
		if got := aisdk.FinishReason(in); got != out {
			t.Errorf("FinishReason(%q) = %q, want %q", in, got, out)
		}
	}
}

func TestUsageGoesIntoFinishMetadataInAISDKShape(t *testing.T) {
	cs := chunks(t, "usage_with_cache_and_reasoning")
	var fin map[string]any
	for _, c := range cs {
		if c["type"] == "finish" {
			fin = c
		}
	}
	md := fin["messageMetadata"].(map[string]any)
	got, _ := json.Marshal(md["usage"])
	want := `{"inputTokenDetails":{"cacheReadTokens":600,"cacheWriteTokens":50,"noCacheTokens":400},"inputTokens":1050,` +
		`"outputTokenDetails":{"reasoningTokens":50,"textTokens":150},"outputTokens":200,"totalTokens":1250}`
	if string(got) != want {
		t.Errorf("usage = %s\nwant %s", got, want)
	}
	if md["rawFinishReason"] != "max_tokens" {
		t.Errorf("rawFinishReason = %v", md["rawFinishReason"])
	}
	// the totals are exactly the disjoint components' sum: no double count
	u := chatstream.Usage{UncachedInput: 400, CacheRead: 600, CacheWrite: 50, Output: 150, Reasoning: 50}
	m := aisdk.UsageJSON(u)
	if m["totalTokens"] != u.Total() || m["inputTokens"] != u.Input() || m["outputTokens"] != u.Generated() {
		t.Errorf("UsageJSON = %v", m)
	}
}

func TestUsageEventsAreQuietAndDeltasAccumulate(t *testing.T) {
	cs := playEvents(t, scenario(t, func(b *sinktest.Builder) {
		b.Start()
		b.UsageDelta(chatstream.Usage{Scope: chatstream.UsageDelta, UncachedInput: 5, Output: 1})
		b.UsageDelta(chatstream.Usage{Scope: chatstream.UsageDelta, Output: 4})
		b.Finish(chatstream.FinishStop, "", nil)
	}))
	if count(cs, "start-step") != 0 {
		t.Errorf("usage alone must not open a step: %v", types(cs))
	}
	fin := cs[len(cs)-2]
	u := fin["messageMetadata"].(map[string]any)["usage"].(map[string]any)
	if u["inputTokens"] != float64(5) || u["outputTokens"] != float64(5) || u["totalTokens"] != float64(10) {
		t.Errorf("usage = %v", u)
	}
}

func TestErrorAndAbortEndWithoutAFinishChunk(t *testing.T) {
	for name, want := range map[string]string{"error_terminal": "error", "abort_terminal": "abort"} {
		cs := chunks(t, name)
		if count(cs, "finish") != 0 || count(cs, want) != 1 {
			t.Errorf("%s: %v", name, types(cs))
		}
	}
	cs := chunks(t, "error_terminal")
	if cs[len(cs)-2]["errorText"] != "slow down" {
		t.Errorf("errorText = %v", cs[len(cs)-2])
	}
}

func TestTextSurvivesFramingIntact(t *testing.T) {
	frames := sinktest.Play(t, newEnc(), sinktest.Named(t, "escaping_and_unicode"))
	got := sinktest.JoinedText(t, frames, "type", []string{"text-delta"}, "delta")
	if got != sinktest.EscapingText() {
		t.Errorf("text = %q\nwant %q", got, sinktest.EscapingText())
	}
	for _, f := range frames {
		if strings.ContainsAny(f.Data, "\r\n") && f.Data != "[DONE]" {
			t.Errorf("a raw newline reached the SSE data: %q", f.Data)
		}
		if strings.Contains(f.Data, "\\u003c") {
			t.Errorf("HTML escaping should be off: %s", f.Data)
		}
	}
}

func TestSourceFileAndDataPartsAreSingleChunks(t *testing.T) {
	cs := chunks(t, "source_file_data_parts")
	for _, ty := range []string{"source-url", "file", "data-weather"} {
		if count(cs, ty) != 1 {
			t.Errorf("%s: %v", ty, types(cs))
		}
	}
	for _, c := range cs {
		if c["type"] == "source-url" && (c["url"] != "https://example.com/a" || c["sourceId"] != "s1" || c["title"] != "A") {
			t.Errorf("source-url = %v", c)
		}
		if c["type"] == "file" && c["mediaType"] != "image/png" {
			t.Errorf("file = %v", c)
		}
	}
}

func TestStepsOpenLazilyAndOnlyOnce(t *testing.T) {
	cs := chunks(t, "plain_text")
	if count(cs, "start-step") != 1 || count(cs, "finish-step") != 1 {
		t.Errorf("chunks = %v", types(cs))
	}
	if types(cs)[0] != "start" {
		t.Errorf("first chunk %s", types(cs)[0])
	}
}

const sinkReplace = "chatstream.replace_content"

func scenario(t *testing.T, f func(b *sinktest.Builder)) []chatstream.Event {
	t.Helper()
	return sinktest.Build(f)
}

func playEvents(t *testing.T, evs []chatstream.Event) []map[string]any {
	t.Helper()
	sc := sinktest.Scenario{Name: "custom", Events: evs}
	var out []map[string]any
	for _, f := range sinktest.Play(t, newEnc(), sc) {
		if f.Data == "[DONE]" {
			out = append(out, map[string]any{"type": "[DONE]"})
			continue
		}
		out = append(out, sinktest.MustJSON(t, f))
	}
	return out
}

// An approval that names the call by id binds to it even when the approval's own
// descriptor does not name the tool, and the call's later announcement does not
// open a second part.
func TestApprovalBindsByCallIDWhenToolNamesDiffer(t *testing.T) {
	cs := playEvents(t, scenario(t, func(b *sinktest.Builder) {
		b.Start()
		b.Approval("ap", "c1", chatstream.ApprovalInBand, `{}`)
		b.Part("c1", chatstream.PartToolCall, sinktest.Meta("name", "bash"))
		b.End("c1", `{"cmd":"ls"}`)
		b.Finish(chatstream.FinishToolCalls, "", nil)
	}))
	if count(cs, "tool-input-available") != 1 || count(cs, "tool-input-start") != 0 || count(cs, "tool-approval-request") != 1 {
		t.Errorf("chunks = %v", types(cs))
	}
}

func TestOutOfOrderEventsAreErrorsNotSilence(t *testing.T) {
	sinktest.OutOfOrderErrors(t, newEnc)
}

// A tool_call id reused after its part ended is a new call: it opens, streams
// and completes again instead of being dropped.
func TestToolCallIDReusedAfterEndIsANewCall(t *testing.T) {
	cs := playEvents(t, scenario(t, func(b *sinktest.Builder) {
		b.Start()
		b.Part("c1", chatstream.PartToolCall, sinktest.Meta("name", "bash"))
		b.End("c1", `{"cmd":"ls"}`)
		b.Part("r1", chatstream.PartToolResult, sinktest.Meta("call_id", "c1"))
		b.Text("r1", "a")
		b.End("r1", "")
		b.Part("c1", chatstream.PartToolCall, sinktest.Meta("name", "bash"))
		b.End("c1", `{"cmd":"pwd"}`)
		b.Finish(chatstream.FinishToolCalls, "", nil)
	}))
	if n := count(cs, "tool-input-start"); n != 2 {
		t.Errorf("tool-input-start = %d, want 2: %v", n, types(cs))
	}
	if n := count(cs, "tool-input-available"); n != 2 {
		t.Errorf("tool-input-available = %d, want 2: %v", n, types(cs))
	}
}

// An approval or a replace_content closes the open text block on the wire, but
// the part is still open: later deltas open a new block and are not dropped, and
// the part's own end is not out of order.
func TestTextDeltaAfterApprovalOrReplaceIsNotLost(t *testing.T) {
	for name, mid := range map[string]func(b *sinktest.Builder){
		"approval": func(b *sinktest.Builder) { b.Approval("ap", "c1", chatstream.ApprovalInBand, `{"tool":"bash"}`) },
		"replace":  func(b *sinktest.Builder) { b.Activity(sinkReplace, `{"content":""}`) },
	} {
		cs := playEvents(t, scenario(t, func(b *sinktest.Builder) {
			b.Start()
			b.Part("t1", chatstream.PartText, nil)
			b.Text("t1", "before ")
			mid(b)
			b.Text("t1", "after")
			b.End("t1", "")
			b.Finish(chatstream.FinishStop, "", nil)
		}))
		got := sinktest.JoinedText(t, framesOf(cs), "type", []string{"text-delta"}, "delta")
		if got != "before after" {
			t.Errorf("%s: text = %q, want %q: %v", name, got, "before after", types(cs))
		}
		if count(cs, "text-start") != count(cs, "text-end") {
			t.Errorf("%s: unbalanced text blocks: %v", name, types(cs))
		}
	}
}

func framesOf(cs []map[string]any) []sinktest.Frame {
	var out []sinktest.Frame
	for _, c := range cs {
		b, _ := json.Marshal(c)
		out = append(out, sinktest.Frame{Data: string(b)})
	}
	return out
}
