package nanitelegacy_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui-go/chatstream/sink/nanitelegacy"
	"github.com/hollis-labs/libs/ui-go/chatstream/sink/sinktest"
)

// stopCancelled is the stop_reason Nanite clients see for an aborted run.
const stopCancelled = "cancelled" //nolint:misspell // the wire value

func frames(t *testing.T, name string) []sinktest.Frame {
	t.Helper()
	return sinktest.Play(t, newEnc(), sinktest.Named(t, name))
}

func playEvents(t *testing.T, evs []chatstream.Event) []sinktest.Frame {
	return sinktest.Play(t, newEnc(), sinktest.Scenario{Name: "custom", Events: evs})
}

func find(fs []sinktest.Frame, event string) []sinktest.Frame {
	var out []sinktest.Frame
	for _, f := range fs {
		if f.Event == event {
			out = append(out, f)
		}
	}
	return out
}

// Nanite's framing: the SSE event name is the type, the data JSON repeats it,
// and event_id is the event's Seq, also the frame id.
func TestFramingMatchesNanite(t *testing.T) {
	for _, sc := range sinktest.Scenarios() {
		for _, f := range sinktest.Play(t, newEnc(), sc) {
			m := sinktest.MustJSON(t, f)
			if m["type"] != f.Event {
				t.Errorf("%s: type %v != event %q", sc.Name, m["type"], f.Event)
			}
			if id, ok := m["event_id"]; ok && f.ID == "" {
				t.Errorf("%s: event_id %v without an id line", sc.Name, id)
			}
		}
	}
	fs := frames(t, "plain_text")
	m := sinktest.MustJSON(t, fs[0])
	if fs[0].ID != "1" || m["event_id"] != float64(1) || m["message_id"] != "run-1" {
		t.Errorf("stream_start = %+v %v", fs[0], m)
	}
	// an event with no Seq is written without an id and without event_id
	ev := sinktest.Named(t, "plain_text").Events[0]
	ev.Seq = 0
	got := playEvents(t, []chatstream.Event{ev})
	m = sinktest.MustJSON(t, got[0])
	if got[0].ID != "" || m["event_id"] != nil {
		t.Errorf("seq-less frame = %+v", got[0])
	}
}

func TestPhaseComesFromThePartMeta(t *testing.T) {
	fs := playEvents(t, sinktest.Build(func(b *sinktest.Builder) {
		b.Start()
		b.Part("n", chatstream.PartText, sinktest.Meta("phase", "narration"))
		b.Text("n", "first")
		b.End("n", "")
		b.Part("f", chatstream.PartText, sinktest.Meta("phase", "final"))
		b.Text("f", "second")
		b.End("f", "")
		b.Part("u", chatstream.PartText, nil)
		b.Text("u", "live")
		b.End("u", "")
		b.Finish(chatstream.FinishStop, "", nil)
	}))
	var got []string
	for _, f := range find(fs, "delta") {
		m := sinktest.MustJSON(t, f)
		p, _ := m["phase"].(string)
		got = append(got, p+":"+m["content"].(string))
	}
	if strings.Join(got, ",") != "narration:first,final:second,:live" {
		t.Errorf("deltas = %v", got)
	}
}

func TestApprovalDataIsADoubleEncodedJSONString(t *testing.T) {
	f := find(frames(t, "approval_inband_after_call"), "approval_request")[0]
	m := sinktest.MustJSON(t, f)
	s, ok := m["data"].(string)
	if !ok {
		t.Fatalf("data is %T, want a JSON string (Nanite double-encodes it)", m["data"])
	}
	var p struct {
		RequestID string          `json:"request_id"`
		Tool      string          `json:"tool"`
		Input     json.RawMessage `json:"input"`
		Reason    string          `json:"reason"`
	}
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		t.Fatal(err)
	}
	if p.RequestID != "ap-1" || p.Tool != "bash" || string(p.Input) != `{"cmd":"ls"}` || p.Reason != "runs a shell command" {
		t.Errorf("payload = %+v", p)
	}
}

func TestToolResultSummaryIsTruncatedLikeNanite(t *testing.T) {
	if got := nanitelegacy.Truncate(strings.Repeat("a", 500)); len(got) != 500 {
		t.Errorf("500 bytes must pass unchanged, got %d", len(got))
	}
	long := nanitelegacy.Truncate(strings.Repeat("a", 501))
	if long != strings.Repeat("a", 500)+"... (truncated)" {
		t.Errorf("501 bytes: %q", long)
	}
	// 250 two-byte runes = 500 bytes, plus one more rune: cut on a rune boundary
	multi := nanitelegacy.Truncate(strings.Repeat("é", 251))
	if !strings.HasSuffix(multi, "... (truncated)") || strings.Contains(multi, "�") || len(multi) != 500+len("... (truncated)") {
		t.Errorf("multi-byte cut: len %d %q", len(multi), multi[len(multi)-30:])
	}
	odd := nanitelegacy.Truncate(strings.Repeat("a", 499) + "é" + "z")
	if strings.Contains(odd, "�") || !strings.HasPrefix(odd, strings.Repeat("a", 499)) || !strings.HasSuffix(odd, "... (truncated)") {
		t.Errorf("a rune straddling the limit must be dropped whole: %q", odd)
	}
	if nanitelegacy.SummaryLimit != 500 {
		t.Error("SummaryLimit")
	}
}

func TestStopReasonTable(t *testing.T) {
	cases := []struct {
		r    chatstream.FinishReason
		raw  string
		want string
	}{
		{chatstream.FinishStop, "", "end_turn"}, {chatstream.FinishLength, "", "max_tokens"}, {chatstream.FinishToolCalls, "", "tool_use"},
		{chatstream.FinishRefusal, "", "refusal"}, {chatstream.FinishPause, "", "pause"},
		{chatstream.FinishStop, "stop_sequence", "stop_sequence"}, {chatstream.FinishToolCalls, "tool_use", "tool_use"},
	}
	for _, c := range cases {
		if got := nanitelegacy.StopReason(c.r, c.raw); got != c.want {
			t.Errorf("StopReason(%s, %q) = %q, want %q", c.r, c.raw, got, c.want)
		}
	}
}

func TestUsageMapsToNanitesAnthropicStyleCounts(t *testing.T) {
	end := find(frames(t, "usage_with_cache_and_reasoning"), "stream_end")[0]
	u := sinktest.MustJSON(t, end)["usage"].(map[string]any)
	// input excludes cache, output includes reasoning
	want := map[string]float64{"input_tokens": 400, "output_tokens": 200, "cache_read_tokens": 600, "cache_creation_tokens": 50}
	for k, v := range want {
		if u[k] != v {
			t.Errorf("usage.%s = %v, want %v", k, u[k], v)
		}
	}
	if u["stop_reason"] != "max_tokens" {
		t.Errorf("stop_reason = %v", u["stop_reason"])
	}
}

func TestErrorIsTerminalWithNoStreamEnd(t *testing.T) {
	fs := frames(t, "error_terminal")
	if len(find(fs, "stream_end")) != 0 || len(find(fs, "error")) != 1 {
		t.Errorf("frames = %v", fs)
	}
	m := sinktest.MustJSON(t, find(fs, "error")[0])
	se := m["structured_error"].(map[string]any)
	if m["error"] != "slow down" || se["code"] != "rate_limited" || se["message"] != "slow down" {
		t.Errorf("error = %v", m)
	}
}

func TestAbortIsAStatusNoticeThenStreamEndCancelled(t *testing.T) {
	fs := frames(t, "abort_terminal")
	n := len(fs)
	if fs[n-2].Event != "status" || fs[n-1].Event != "stream_end" {
		t.Fatalf("frames = %v", sinktest.Render(fs))
	}
	if c := sinktest.MustJSON(t, fs[n-2])["content"]; c != "run aborted: user canceled" {
		t.Errorf("status = %v", c)
	}
	if r := sinktest.MustJSON(t, fs[n-1])["usage"].(map[string]any)["stop_reason"]; r != stopCancelled {
		t.Errorf("stop_reason = %v", r)
	}
}

// App-specific Nanite events that arrived as raw{dialect: nanite} go back out
// unchanged under their own event name; other dialects' raw events are dropped.
func TestNaniteRawPassesThroughUnchanged(t *testing.T) {
	fs := frames(t, "raw_and_activity")
	slot := find(fs, "slot_changed")
	if len(slot) != 1 || slot[0].Data != `{"type":"slot_changed","content":"slot 2"}` {
		t.Fatalf("slot_changed = %+v", slot)
	}
	if len(find(fs, "ping")) != 0 {
		t.Error("an anthropic raw event must not be forwarded")
	}
	rc := find(fs, "replace_content")
	if len(rc) != 1 || sinktest.MustJSON(t, rc[0])["content"] != "replaced" {
		t.Errorf("replace_content = %+v", rc)
	}
}

func TestToolCallDetailAndResultFlag(t *testing.T) {
	tc := sinktest.MustJSON(t, find(frames(t, "tool_call_and_result"), "tool_call")[0])
	if tc["tool"] != "get_weather" || tc["tool_id"] != "c1" || tc["detail"] != "Oslo" {
		t.Errorf("tool_call = %v", tc)
	}
	if _, has := tc["input"]; has {
		t.Error("Nanite does not stream tool input")
	}
	ok := sinktest.MustJSON(t, find(frames(t, "tool_call_and_result"), "tool_result")[0])
	if _, has := ok["is_error"]; has {
		t.Errorf("a successful result must not carry is_error: %v", ok)
	}
	bad := sinktest.MustJSON(t, find(frames(t, "tool_result_error_and_whole_args"), "tool_result")[0])
	if bad["is_error"] != true || bad["summary"] != "exit status 1" {
		t.Errorf("tool_result = %v", bad)
	}
}

func TestCloseWithoutTerminalIsAnErrorEvent(t *testing.T) {
	fs := frames(t, "truncated_mid_text")
	e := find(fs, "error")
	if len(e) != 1 || fs[len(fs)-1].Event != "error" {
		t.Fatalf("frames = %v", sinktest.Render(fs))
	}
	m := sinktest.MustJSON(t, e[0])
	if m["structured_error"].(map[string]any)["code"] != "stream_lost" || !strings.Contains(m["error"].(string), "connection reset by peer") {
		t.Errorf("error = %v", m)
	}
}

func TestGapIsAStatusNotice(t *testing.T) {
	st := find(frames(t, "gap_in_stream"), "status")
	if len(st) != 1 || sinktest.MustJSON(t, st[0])["content"] != "events 5..9 are missing (retention)" {
		t.Errorf("status = %+v", st)
	}
}

func TestTextSurvivesFramingIntact(t *testing.T) {
	fs := frames(t, "escaping_and_unicode")
	if got := sinktest.JoinedText(t, fs, "type", []string{"delta"}, "content"); got != sinktest.EscapingText() {
		t.Errorf("text = %q", got)
	}
	for _, f := range fs {
		if strings.ContainsAny(f.Data, "\r\n") {
			t.Errorf("raw newline in data: %q", f.Data)
		}
	}
}

func TestHeadersMatchNanite(t *testing.T) {
	h := nanitelegacy.New().Headers()
	if h.Get("Content-Type") != "text/event-stream" || h.Get("Connection") != "keep-alive" || nanitelegacy.New().Name() != "nanitelegacy" {
		t.Errorf("headers = %v", h)
	}
}

func TestOutOfOrderEventsAreErrorsNotSilence(t *testing.T) {
	sinktest.OutOfOrderErrors(t, newEnc)
}

// A nanite raw payload that is not JSON is refused: written as a data line, a
// newline in it would inject SSE fields.
func TestRawPayloadThatIsNotJSONIsRefused(t *testing.T) {
	enc := nanitelegacy.New()
	rec := &sinktest.Recorder{}
	if err := enc.Encode(rec, sinktest.Build(func(b *sinktest.Builder) { b.Start() })[0]); err != nil {
		t.Fatal(err)
	}
	before := rec.Len()
	err := enc.Encode(rec, chatstream.Event{V: chatstream.SchemaVersion, RunID: "run-1", Verb: chatstream.VerbRaw,
		Raw: &chatstream.Raw{Dialect: "nanite", Type: "status", Payload: json.RawMessage("not json\nevent: stream_end\ndata: {}")}})
	if !errors.Is(err, nanitelegacy.ErrInvalidPayload) {
		t.Fatalf("err = %v, want ErrInvalidPayload", err)
	}
	if rec.Len() != before {
		t.Errorf("wrote %q for a refused payload", rec.String()[before:])
	}
}
