package agui_test

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/sink/agui"
	"github.com/hollis-labs/go-chatstream/sink/sinktest"
)

// outcomeCancel is AG-UI's wire spelling of the cancel outcome.
const outcomeCancel = "cancelled" //nolint:misspell // the wire value

func events(t *testing.T, sc sinktest.Scenario) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, f := range sinktest.Play(t, newEnc(), sc) {
		out = append(out, sinktest.MustJSON(t, f))
	}
	return out
}

func play(t *testing.T, evs []chatstream.Event) []map[string]any {
	return events(t, sinktest.Scenario{Name: "custom", Events: evs})
}

func typeList(es []map[string]any) []string {
	var out []string
	for _, e := range es {
		out = append(out, e["type"].(string))
	}
	return out
}

// checkAGUI applies AG-UI's ordering rules (lifecycle and streaming pages) to a
// stream: it begins with RUN_STARTED or RUN_ERROR, nothing follows the terminal
// event, an id is never opened twice while open, content and end need an open
// id, and everything opened is closed before RUN_FINISHED.
func checkAGUI(t *testing.T, name string, es []map[string]any) {
	t.Helper()
	open := map[string]bool{}
	terminal := false
	for i, e := range es {
		ty := e["type"].(string)
		fail := func(format string, a ...any) {
			t.Errorf("%s: event %d %s: %s", name, i, ty, fmt.Sprintf(format, a...))
		}
		if terminal {
			fail("follows the terminal event")
			return
		}
		if i == 0 && ty != "RUN_STARTED" && ty != "RUN_ERROR" {
			fail("a stream must begin with RUN_STARTED or RUN_ERROR")
		}
		key := func(prefix, field string) string { return prefix + ":" + fmt.Sprint(e[field]) }
		opens := map[string][2]string{
			"TEXT_MESSAGE_START": {"text", "messageId"}, "TOOL_CALL_START": {"tool", "toolCallId"},
			"REASONING_START": {"reasoning", "messageId"}, "REASONING_MESSAGE_START": {"reasoningmsg", "messageId"},
		}
		contents := map[string][2]string{
			"TEXT_MESSAGE_CONTENT": {"text", "messageId"}, "TEXT_MESSAGE_END": {"text", "messageId"},
			"TOOL_CALL_ARGS": {"tool", "toolCallId"}, "TOOL_CALL_END": {"tool", "toolCallId"},
			"REASONING_MESSAGE_CONTENT": {"reasoningmsg", "messageId"}, "REASONING_MESSAGE_END": {"reasoningmsg", "messageId"},
			"REASONING_END": {"reasoning", "messageId"},
		}
		if o, ok := opens[ty]; ok {
			k := key(o[0], o[1])
			if open[k] {
				fail("%s is already open", k)
			}
			open[k] = true
		}
		if c, ok := contents[ty]; ok {
			k := key(c[0], c[1])
			if !open[k] {
				fail("%s is not open", k)
			}
			if strings.HasSuffix(ty, "_END") {
				delete(open, k)
			}
		}
		if ty == "STEP_STARTED" {
			k := "step:" + fmt.Sprint(e["stepName"])
			if open[k] {
				fail("step already open")
			}
			open[k] = true
		}
		if ty == "STEP_FINISHED" {
			k := "step:" + fmt.Sprint(e["stepName"])
			if !open[k] {
				fail("step not open")
			}
			delete(open, k)
		}
		if ty == "RUN_FINISHED" {
			terminal = true
			for k := range open {
				fail("%s is still open at RUN_FINISHED", k)
			}
		}
		if ty == "RUN_ERROR" {
			terminal = true
		}
		if ty == "RUN_STARTED" && (e["threadId"] == nil || e["runId"] == nil) {
			fail("RUN_STARTED needs threadId and runId")
		}
	}
	if !terminal {
		t.Errorf("%s: the stream has no terminal event", name)
	}
}

func TestEveryScenarioIsAValidAGUIStream(t *testing.T) {
	for _, sc := range sinktest.Scenarios() {
		checkAGUI(t, sc.Name, events(t, sc))
	}
}

func TestSeqIsCarriedInTheFrameIDAndTimestampIsMilliseconds(t *testing.T) {
	sc := sinktest.Named(t, "plain_text")
	frames := sinktest.Play(t, newEnc(), sc)
	if frames[0].ID != "1" || frames[1].ID != "3" {
		t.Errorf("ids = %q %q", frames[0].ID, frames[1].ID)
	}
	for _, f := range frames {
		m := sinktest.MustJSON(t, f)
		if m["timestamp"] != float64(sinktest.T0.UnixMilli()) {
			t.Errorf("timestamp = %v", m["timestamp"])
		}
		if f.Event != "" {
			t.Errorf("AG-UI frames carry no event name, got %q", f.Event)
		}
	}
}

func TestRunStartedCarriesThreadAndParent(t *testing.T) {
	enc := agui.New(agui.WithThreadID("thread-9"))
	rec := &sinktest.Recorder{}
	ev := sinktest.Named(t, "plain_text").Events[0]
	ev.ParentRunID = "run-0"
	if err := enc.Encode(rec, ev); err != nil {
		t.Fatal(err)
	}
	m := sinktest.MustJSON(t, sinktest.Parse(t, rec.Bytes())[0])
	if m["threadId"] != "thread-9" || m["runId"] != "run-1" || m["parentRunId"] != "run-0" {
		t.Errorf("RUN_STARTED = %v", m)
	}
	// default thread id is the run id
	es := events(t, sinktest.Named(t, "plain_text"))
	if es[0]["threadId"] != "run-1" {
		t.Errorf("default threadId = %v", es[0]["threadId"])
	}
}

func TestOutcomes(t *testing.T) {
	last := func(name string) map[string]any { es := events(t, sinktest.Named(t, name)); return es[len(es)-1] }

	if _, has := last("plain_text")["outcome"]; has {
		t.Error("a success run has no outcome (absent means success)")
	}
	if o := last("abort_terminal")["outcome"].(map[string]any); o["type"] != outcomeCancel {
		t.Errorf("abort outcome = %v", o)
	}
	o := last("approval_suspend")["outcome"].(map[string]any)
	if o["type"] != "interrupt" {
		t.Fatalf("suspend outcome = %v", o)
	}
	ints := o["interrupts"].([]any)
	in := ints[0].(map[string]any)
	if len(ints) != 1 || in["id"] != "ap-2" || in["reason"] != "runs a shell command" || in["toolCallId"] != "c1" || in["expiresAt"] != "2026-01-02T03:09:05Z" {
		t.Errorf("interrupts = %v", ints)
	}
	// a canceled finish reason is a cancel outcome, not success
	es := play(t, sinktest.Build(func(b *sinktest.Builder) {
		b.Start()
		b.Finish(chatstream.FinishCancelled, "", nil)
	}))
	if es[len(es)-1]["outcome"].(map[string]any)["type"] != outcomeCancel {
		t.Errorf("canceled finish = %v", es[len(es)-1])
	}
	// an in-band approval does not interrupt
	if _, has := last("approval_inband_after_call")["outcome"]; has {
		t.Error("an in-band approval must not produce an interrupt outcome")
	}
}

func TestRunErrorEndsWhateverIsOpenWithoutClosingIt(t *testing.T) {
	es := play(t, sinktest.Build(func(b *sinktest.Builder) {
		b.Start()
		b.Part("t1", chatstream.PartText, nil)
		b.Text("t1", "x")
		b.Add(chatstream.VerbRunError, func(e *chatstream.Event) { e.Code, e.Message = "boom", "it broke" })
	}))
	ts := typeList(es)
	if ts[len(ts)-1] != "RUN_ERROR" || strings.Contains(strings.Join(ts, ","), "TEXT_MESSAGE_END") {
		t.Errorf("types = %v; RUN_ERROR ends open items, the encoder does not have to close them", ts)
	}
	if es[len(es)-1]["message"] != "it broke" || es[len(es)-1]["code"] != "boom" {
		t.Errorf("RUN_ERROR = %v", es[len(es)-1])
	}
}

func TestFinishClosesWhatIsStillOpenBeforeRunFinished(t *testing.T) {
	es := play(t, sinktest.Build(func(b *sinktest.Builder) {
		b.Start()
		b.Add(chatstream.VerbStepStart, func(e *chatstream.Event) { e.StepID = "s" })
		b.Part("t1", chatstream.PartText, nil)
		b.Text("t1", "x")
		b.Part("c1", chatstream.PartToolCall, sinktest.Meta("name", "bash"))
		b.Finish(chatstream.FinishStop, "", nil)
	}))
	checkAGUI(t, "unclosed", es)
	ts := typeList(es)
	if ts[len(ts)-1] != "RUN_FINISHED" || ts[len(ts)-2] != "STEP_FINISHED" {
		t.Errorf("types = %v", ts)
	}
}

func TestUsageShapeAndOmittedZeroes(t *testing.T) {
	es := events(t, sinktest.Named(t, "usage_with_cache_and_reasoning"))
	fin := es[len(es)-1]
	raw, _ := json.Marshal(fin["usage"])
	want := `[{"cacheWriteInputTokens":50,"cachedInputTokens":600,"inputTokens":1050,"model":"claude-x","outputTokens":200,"provider":"anthropic","reasoningTokens":50,"totalTokens":1250}]`
	if string(raw) != want {
		t.Errorf("usage = %s\nwant %s", raw, want)
	}
	// inputTokens includes cache, outputTokens includes reasoning: parts, never additions
	u := fin["usage"].([]any)[0].(map[string]any)
	if u["inputTokens"].(float64) < u["cachedInputTokens"].(float64)+u["cacheWriteInputTokens"].(float64) ||
		u["outputTokens"].(float64) < u["reasoningTokens"].(float64) {
		t.Errorf("subset counts exceed totals: %v", u)
	}
	plain := events(t, sinktest.Named(t, "plain_text"))
	if _, has := plain[len(plain)-1]["usage"]; has {
		t.Error("no usage reported: the field must be absent, not zero")
	}
	// RUN_ERROR carries usage too
	es = play(t, sinktest.Build(func(b *sinktest.Builder) {
		b.Start()
		b.UsageDelta(chatstream.Usage{Scope: chatstream.UsageFinal, UncachedInput: 3, Output: 2})
		b.Add(chatstream.VerbRunError, func(e *chatstream.Event) { e.Code, e.Message = "x", "y" })
	}))
	if es[len(es)-1]["usage"] == nil {
		t.Errorf("RUN_ERROR = %v", es[len(es)-1])
	}
}

func TestReasoningEncryptedValueAndWholeToolArguments(t *testing.T) {
	es := events(t, sinktest.Named(t, "reasoning_then_text"))
	var enc map[string]any
	for _, e := range es {
		if e["type"] == "REASONING_ENCRYPTED_VALUE" {
			enc = e
		}
	}
	if enc == nil || enc["encryptedValue"] != "sig-abc" || enc["entityId"] != "r1" || enc["subtype"] != "message" {
		t.Errorf("encrypted value = %v", enc)
	}
	es = events(t, sinktest.Named(t, "tool_result_error_and_whole_args"))
	ts := typeList(es)
	if strings.Join(ts[1:4], ",") != "TOOL_CALL_START,TOOL_CALL_ARGS,TOOL_CALL_END" || es[2]["delta"] != `{"cmd":"false"}` {
		t.Errorf("whole arguments must become one TOOL_CALL_ARGS: %v", es)
	}
}

func TestToolResultMintsAToolMessage(t *testing.T) {
	es := events(t, sinktest.Named(t, "tool_call_and_result"))
	for _, e := range es {
		if e["type"] == "TOOL_CALL_RESULT" {
			if e["role"] != "tool" || e["toolCallId"] != "c1" || e["messageId"] != "res1" || e["content"] != "sunny, 21C" {
				t.Errorf("TOOL_CALL_RESULT = %v", e)
			}
			return
		}
	}
	t.Error("no TOOL_CALL_RESULT")
}

func TestActivityStateRawAndGap(t *testing.T) {
	es := events(t, sinktest.Named(t, "raw_and_activity"))
	byType := map[string]map[string]any{}
	for _, e := range es {
		byType[e["type"].(string)] = e
	}
	if byType["RAW"]["source"] == nil {
		t.Errorf("RAW = %v", byType["RAW"])
	}
	snap := byType["ACTIVITY_SNAPSHOT"]
	if snap["activityType"] != "nanite.status" || snap["replace"] != true || snap["messageId"] != byType["ACTIVITY_DELTA"]["messageId"] {
		t.Errorf("activity snapshot %v / delta %v", snap, byType["ACTIVITY_DELTA"])
	}
	if byType["STATE_SNAPSHOT"] == nil {
		t.Error("no STATE_SNAPSHOT")
	}
	gap := events(t, sinktest.Named(t, "gap_in_stream"))
	found := false
	for _, e := range gap {
		if e["type"] == "CUSTOM" && e["name"] == "chatstream.gap" {
			found = true
			if v := e["value"].(map[string]any); v["from"] != float64(5) || v["to"] != float64(9) || v["reason"] != "retention" {
				t.Errorf("gap value = %v", v)
			}
		}
	}
	if !found {
		t.Error("no gap CUSTOM event")
	}
	// a non-object activity value is wrapped: ACTIVITY_SNAPSHOT content must be an object
	es = play(t, sinktest.Build(func(b *sinktest.Builder) {
		b.Start()
		b.Activity("app.progress", `42`)
		b.Finish(chatstream.FinishStop, "", nil)
	}))
	c := es[1]["content"].(map[string]any)
	if c["value"] != float64(42) {
		t.Errorf("content = %v", c)
	}
}

func TestInBandApprovalIsACustomEvent(t *testing.T) {
	es := events(t, sinktest.Named(t, "approval_inband_after_call"))
	for _, e := range es {
		if e["type"] == "CUSTOM" && e["name"] == "chatstream.approval_request" {
			v := e["value"].(map[string]any)
			if v["approvalId"] != "ap-1" || v["toolCallId"] != "c1" {
				t.Errorf("value = %v", v)
			}
			return
		}
	}
	t.Error("no approval CUSTOM event")
}

func TestCloseWithoutTerminalIsRunErrorDistinguishableFromTruncation(t *testing.T) {
	es := events(t, sinktest.Named(t, "truncated_mid_text"))
	checkAGUI(t, "truncated", es)
	last := es[len(es)-1]
	if last["type"] != "RUN_ERROR" || last["code"] != "stream_lost" || !strings.Contains(last["message"].(string), "connection reset by peer") {
		t.Errorf("last = %v", last)
	}
	// and a stream that never started begins with RUN_ERROR, which AG-UI allows
	es = events(t, sinktest.Named(t, "truncated_before_anything"))
	if len(es) != 1 || es[0]["type"] != "RUN_ERROR" {
		t.Errorf("events = %v", es)
	}
}

func TestTextSurvivesFramingIntact(t *testing.T) {
	frames := sinktest.Play(t, newEnc(), sinktest.Named(t, "escaping_and_unicode"))
	if got := sinktest.JoinedText(t, frames, "type", []string{"TEXT_MESSAGE_CONTENT"}, "delta"); got != sinktest.EscapingText() {
		t.Errorf("text = %q", got)
	}
	for _, f := range frames {
		if strings.ContainsAny(f.Data, "\r\n") {
			t.Errorf("raw newline in data: %q", f.Data)
		}
		if _, err := strconv.Atoi(f.ID); f.ID != "" && err != nil {
			t.Errorf("id %q", f.ID)
		}
	}
}

func TestHeaders(t *testing.T) {
	h := agui.New().Headers()
	if h.Get("Content-Type") != "text/event-stream" || agui.New().Name() != "agui" {
		t.Errorf("headers = %v", h)
	}
}
