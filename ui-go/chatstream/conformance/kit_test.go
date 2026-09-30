package conformance_test

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/conformance"
	"github.com/hollis-labs/go-chatstream/internal/decodekit"
)

var t0 = conformance.FixedTime

func e(verb chatstream.Verb, f func(*chatstream.Event)) chatstream.Event {
	ev := chatstream.Event{V: chatstream.SchemaVersion, RunID: "r", Time: t0, Verb: verb}
	if f != nil {
		f(&ev)
	}
	return ev
}

func goodStream() []chatstream.Event {
	return []chatstream.Event{
		e(chatstream.VerbRunStart, nil),
		e(chatstream.VerbMessageStart, func(x *chatstream.Event) { x.MessageID, x.Role = "m", "assistant" }),
		e(chatstream.VerbPartStart, func(x *chatstream.Event) { x.PartID, x.Kind = "p", "text" }),
		e(chatstream.VerbPartDelta, func(x *chatstream.Event) { x.PartID, x.Text = "p", "héllo" }),
		e(chatstream.VerbPartEnd, func(x *chatstream.Event) { x.PartID = "p" }),
		e(chatstream.VerbMessageEnd, nil),
		e(chatstream.VerbRunFinish, func(x *chatstream.Event) { x.Reason = "stop" }),
	}
}

func TestValidateAcceptsAWellFormedStream(t *testing.T) {
	if v := conformance.Validate(goodStream()); len(v) != 0 {
		t.Fatalf("violations: %v", v)
	}
}

// Each mutation breaks exactly one invariant, and Validate names it.
func TestValidateCatchesEachViolation(t *testing.T) {
	part := func(id string, k string) chatstream.Event {
		return e(chatstream.VerbPartStart, func(x *chatstream.Event) { x.PartID, x.Kind = id, k })
	}
	end := func(id string) chatstream.Event {
		return e(chatstream.VerbPartEnd, func(x *chatstream.Event) { x.PartID = id })
	}
	fin := e(chatstream.VerbRunFinish, func(x *chatstream.Event) { x.Reason = "stop" })
	start := e(chatstream.VerbRunStart, nil)
	tests := []struct {
		name   string
		events []chatstream.Event
		opts   []conformance.Options
		rule   string
		frag   string
	}{
		{"no terminal", []chatstream.Event{start}, nil, conformance.RuleTerminal, "no terminal"},
		{"two terminals", []chatstream.Event{start, fin, fin}, nil, conformance.RuleTerminal, "follows the terminal"},
		{"event after error", []chatstream.Event{start, e(chatstream.VerbRunError, func(x *chatstream.Event) { x.Code = "c" }), e(chatstream.VerbRaw, nil)}, nil, conformance.RuleTerminal, "follows the terminal"},
		{"open part at terminal", []chatstream.Event{start, part("p", "text"), fin}, nil, conformance.RuleLifecycle, "still open"},
		{"delta on closed part", []chatstream.Event{start, part("p", "text"), end("p"), e(chatstream.VerbPartDelta, func(x *chatstream.Event) { x.PartID, x.Text = "p", "x" }), fin}, nil, conformance.RuleLifecycle, "not open"},
		{"id reuse while open", []chatstream.Event{start, part("p", "text"), part("p", "text"), end("p"), fin}, nil, conformance.RuleLifecycle, "already open"},
		{"text on tool call", []chatstream.Event{start, part("p", "tool_call"), e(chatstream.VerbPartDelta, func(x *chatstream.Event) { x.PartID, x.Text = "p", "x" }), end("p"), fin}, nil, conformance.RulePayload, "no mixed"},
		{"json on text", []chatstream.Event{start, part("p", "text"), e(chatstream.VerbPartDelta, func(x *chatstream.Event) { x.PartID, x.JSONFragment = "p", "{}" }), end("p"), fin}, nil, conformance.RulePayload, "no mixed"},
		{"invalid utf-8", []chatstream.Event{start, part("p", "text"), e(chatstream.VerbPartDelta, func(x *chatstream.Event) { x.PartID, x.Text = "p", "a\xffb" }), end("p"), fin}, nil, conformance.RulePayload, "UTF-8"},
		{"open message at terminal", []chatstream.Event{start, e(chatstream.VerbMessageStart, nil), fin}, nil, conformance.RuleLifecycle, "message is still open"},
		{"open step at terminal", []chatstream.Event{start, e(chatstream.VerbStepStart, func(x *chatstream.Event) { x.StepID = "s" }), fin}, nil, conformance.RuleLifecycle, "step"},
		{"unknown finish reason", []chatstream.Event{start, e(chatstream.VerbRunFinish, func(x *chatstream.Event) { x.Reason = "end_turn" })}, nil, conformance.RuleVocabulary, "closed vocabulary"},
		{"unknown part kind", []chatstream.Event{start, part("p", "hologram"), end("p"), fin}, nil, conformance.RuleVocabulary, "part kind"},
		{"unknown verb", []chatstream.Event{start, e("nope", nil), fin}, nil, conformance.RuleVocabulary, "unknown verb"},
		{"two final usages", []chatstream.Event{start,
			e(chatstream.VerbUsage, func(x *chatstream.Event) { x.Usage = &chatstream.Usage{Scope: chatstream.UsageFinal, Output: 1} }),
			e(chatstream.VerbRunFinish, func(x *chatstream.Event) {
				x.Reason = "stop"
				x.Usage = &chatstream.Usage{Scope: chatstream.UsageFinal, Output: 1}
			})}, nil, conformance.RuleUsage, "more than one final"},
		{"cumulative usage goes down", []chatstream.Event{start,
			e(chatstream.VerbUsage, func(x *chatstream.Event) { x.Usage = &chatstream.Usage{Scope: chatstream.UsageCumulative, Output: 9} }),
			e(chatstream.VerbUsage, func(x *chatstream.Event) { x.Usage = &chatstream.Usage{Scope: chatstream.UsageCumulative, Output: 3} }), fin}, nil, conformance.RuleUsage, "went down"},
		{"negative usage", []chatstream.Event{start, e(chatstream.VerbUsage, func(x *chatstream.Event) { x.Usage = &chatstream.Usage{Scope: chatstream.UsageDelta, Output: -1} }), fin}, nil, conformance.RuleUsage, "negative"},
		{"gap from a decoder", []chatstream.Event{start, e(chatstream.VerbGap, func(x *chatstream.Event) { x.From, x.To = 1, 2 }), fin}, nil, conformance.RuleLifecycle, "gaps come from the hub"},
		{"does not begin with run.start", []chatstream.Event{part("p", "text"), end("p"), fin}, nil, conformance.RuleLifecycle, "does not begin with run.start"},
		{"second run.start", []chatstream.Event{start, start, fin}, nil, conformance.RuleLifecycle, "not the first"},
		{"run id changes", []chatstream.Event{start, {V: chatstream.SchemaVersion, RunID: "other", Time: t0, Verb: chatstream.VerbRaw, Raw: &chatstream.Raw{Dialect: "d"}}, fin}, nil, conformance.RuleEnvelope, "run id"},
		{"seq goes backwards", []chatstream.Event{
			{V: chatstream.SchemaVersion, Seq: 5, RunID: "r", Time: t0, Verb: chatstream.VerbRunStart},
			{V: chatstream.SchemaVersion, Seq: 4, RunID: "r", Time: t0, Verb: chatstream.VerbRunFinish, Reason: "stop"}}, nil, conformance.RuleEnvelope, "seq"},
		{"wrong schema version", []chatstream.Event{{V: "9", RunID: "r", Time: t0, Verb: chatstream.VerbRunStart}, fin}, nil, conformance.RuleEnvelope, "schema version"},
		{"no time", []chatstream.Event{{V: chatstream.SchemaVersion, RunID: "r", Verb: chatstream.VerbRunStart}, fin}, nil, conformance.RuleEnvelope, "no time"},
		{"run.error without code", []chatstream.Event{start, e(chatstream.VerbRunError, nil)}, nil, conformance.RulePayload, "code"},
		{"raw without dialect", []chatstream.Event{start, e(chatstream.VerbRaw, nil), fin}, nil, conformance.RulePayload, "dialect"},
		{"tool_call without a name", []chatstream.Event{start, part("p", "tool_call"), end("p"), fin}, nil, conformance.RuleConvention, "no meta \"name\""},
		{"tool_result without call_id", []chatstream.Event{start, part("p", "tool_result"), end("p"), fin}, nil, conformance.RuleConvention, "no meta \"call_id\""},
		{"tool_result answering an unknown call", []chatstream.Event{start,
			e(chatstream.VerbPartStart, func(x *chatstream.Event) {
				x.PartID, x.Kind = "r", "tool_result"
				x.Meta = map[string]json.RawMessage{chatstream.MetaCallID: json.RawMessage(`"ghost"`)}
			}), end("r"), fin}, nil, conformance.RuleConvention, "not an earlier tool_call"},
		{"approval without mode", []chatstream.Event{start, e(chatstream.VerbApprovalRequest, func(x *chatstream.Event) { x.ApprovalID = "a" }), fin}, nil, conformance.RuleVocabulary, "approval mode"},
	}
	for _, tc := range tests {
		got := conformance.Validate(tc.events, tc.opts...)
		found := false
		for _, v := range got {
			if v.Rule == tc.rule && strings.Contains(v.Msg, tc.frag) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: want a %s violation mentioning %q, got %v", tc.name, tc.rule, tc.frag, got)
		}
	}
}

func TestValidateOptions(t *testing.T) {
	gap := e(chatstream.VerbGap, func(x *chatstream.Event) { x.From, x.To = 1, 2 })
	evs := []chatstream.Event{e(chatstream.VerbRunStart, nil), gap}
	if v := conformance.Validate(evs, conformance.Options{AllowGap: true, IncompleteOK: true}); len(v) != 0 {
		t.Errorf("options should permit a live snapshot with a gap: %v", v)
	}
	open := []chatstream.Event{e(chatstream.VerbRunStart, nil), e(chatstream.VerbPartStart, func(x *chatstream.Event) { x.PartID, x.Kind = "p", "text" }),
		e(chatstream.VerbRunError, func(x *chatstream.Event) { x.Code = "c" })}
	if v := conformance.Validate(open, conformance.Options{AllowOpenAtEnd: true}); len(v) != 0 {
		t.Errorf("AllowOpenAtEnd: %v", v)
	}
}

func TestReplayEquivalenceHoldsForAWellFormedStream(t *testing.T) {
	if err := conformance.CheckReplayEquivalence(goodStream()); err != nil {
		t.Fatal(err)
	}
	if err := conformance.CheckReplayEquivalence([]chatstream.Event{e(chatstream.VerbPartDelta, func(x *chatstream.Event) { x.PartID = "p" })}); err == nil {
		t.Fatal("a stream Reduce rejects must be reported")
	}
}

// echo is a tiny dialect for testing the kit: {"t":"text","v":...},
// {"t":"tool","id":...,"args":...}, {"t":"done"}.
type echo struct{ variant string }

func (echo) Name() string                { return "echo" }
func (echo) Framing() chatstream.Framing { return chatstream.FramingNDJSON }
func (echo) Capabilities() chatstream.Capabilities {
	return chatstream.Capabilities{Framing: chatstream.FramingNDJSON, Text: chatstream.GranularityChunk, Tools: true, ToolArgs: chatstream.GranularityFinal}
}
func (a echo) NewDecoder(o chatstream.DecodeOptions) chatstream.Decoder {
	return &echoDecoder{Base: decodekit.New(o), variant: a.variant}
}

type echoDecoder struct {
	*decodekit.Base
	variant  string
	textOpen bool
	calls    int
}

func (d *echoDecoder) Decode(f chatstream.Frame) ([]chatstream.Event, error) {
	if d.variant != "decode-after-close-ok" {
		if err := d.CheckOpen(); err != nil {
			return nil, err
		}
	}
	var m struct{ T, V, ID, Args string }
	if err := json.Unmarshal(f.Data, &m); err != nil {
		return d.Emit(nil, d.RawEvent("echo", "malformed", f.Data)), nil //nolint:nilerr // a frame that cannot be decoded is preserved as a raw event
	}
	var out []chatstream.Event
	out = d.EnsureStarted(out)
	switch m.T {
	case "text":
		if !d.textOpen {
			s := d.Event(chatstream.VerbPartStart)
			s.PartID, s.Kind = "t", string(chatstream.PartText)
			out = d.Emit(out, s)
			d.textOpen = true
		}
		dl := d.Event(chatstream.VerbPartDelta)
		dl.PartID, dl.Text = "t", m.V
		out = d.Emit(out, dl)
	case "tool":
		s := d.Event(chatstream.VerbPartStart)
		s.PartID, s.Kind = m.ID, string(chatstream.PartToolCall)
		s.Meta = map[string]json.RawMessage{chatstream.MetaName: json.RawMessage(`"lookup"`)}
		out = d.Emit(out, s)
		en := d.Event(chatstream.VerbPartEnd)
		en.PartID, en.Final = m.ID, json.RawMessage(m.Args)
		out = d.Emit(out, en)
	case "done":
		if d.variant != "leave-parts-open" {
			out = d.Unwind(out)
		}
		fin := d.Event(chatstream.VerbRunFinish)
		fin.Reason = "stop"
		out = d.Emit(out, fin)
		if d.variant == "double-terminal" {
			out = append(out, fin)
		}
	}
	return out, nil
}

func (d *echoDecoder) Close(cause error) []chatstream.Event {
	switch d.variant {
	case "success-on-eof":
		if d.Terminated() || d.Closed() {
			return nil
		}
		d.Base.Close(cause)
		fin := d.Event(chatstream.VerbRunFinish)
		fin.Reason = "stop"
		return []chatstream.Event{fin}
	case "not-idempotent":
		d.calls++
		if d.calls >= 2 {
			x := d.Event(chatstream.VerbRunError)
			x.Code, x.Retryable = chatstream.CodeUpstreamTruncated, true
			return []chatstream.Event{x}
		}
		return d.Base.Close(cause)
	case "leave-parts-open":
		if d.Closed() {
			return nil
		}
		d.Base.Close(cause)
		return []chatstream.Event{func() chatstream.Event {
			x := d.Event(chatstream.VerbRunError)
			x.Code, x.Retryable = chatstream.CodeUpstreamTruncated, true
			return x
		}()}
	}
	return d.Base.Close(cause)
}

func TestCheckDecoderPassesACorrectDecoder(t *testing.T) {
	conformance.CheckDecoderDir(t, echo{}, "testdata")
}

func TestCheckTruncationPassesACorrectDecoder(t *testing.T) {
	fx, err := conformance.LoadFixture("testdata/echo.frames.json")
	if err != nil {
		t.Fatal(err)
	}
	conformance.CheckTruncation(t, echo{}, fx)
}

// fakeTB records failures instead of failing the real test, so a check can be
// required to FAIL for a broken decoder.
type fakeTB struct {
	testing.TB
	failed bool
	msgs   []string
}

func (f *fakeTB) Helper()             {}
func (f *fakeTB) Logf(string, ...any) {}
func (f *fakeTB) Errorf(format string, a ...any) {
	f.failed = true
	f.msgs = append(f.msgs, fmt.Sprintf(format, a...))
}
func (f *fakeTB) Error(a ...any)                 { f.failed = true; f.msgs = append(f.msgs, fmt.Sprint(a...)) }
func (f *fakeTB) Fatalf(format string, a ...any) { f.Errorf(format, a...); panic(fatal{}) }
func (f *fakeTB) Fatal(a ...any)                 { f.Error(a...); panic(fatal{}) }
func (f *fakeTB) Failed() bool                   { return f.failed }

type fatal struct{}

// mustFail requires check to fail, and returns what it reported.
func mustFail(t *testing.T, name string, check func(tb testing.TB)) []string {
	t.Helper()
	f := &fakeTB{}
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(fatal); !ok {
					panic(r)
				}
			}
		}()
		check(f)
	}()
	if !f.failed {
		t.Errorf("%s: the conformance check passed a broken decoder", name)
	}
	return f.msgs
}

func TestCheckTruncationCatchesBrokenDecoders(t *testing.T) {
	fx, err := conformance.LoadFixture("testdata/echo.frames.json")
	if err != nil {
		t.Fatal(err)
	}
	wantMsg := map[string]string{
		"success-on-eof":        "never look like success",
		"leave-parts-open":      "still open",
		"not-idempotent":        "second Close",
		"double-terminal":       "terminal",
		"decode-after-close-ok": "ErrDecoderClosed",
	}
	for variant, frag := range wantMsg {
		msgs := mustFail(t, variant, func(tb testing.TB) { conformance.CheckTruncation(tb, echo{variant: variant}, fx) })
		if !strings.Contains(strings.Join(msgs, "\n"), frag) {
			t.Errorf("%s: failure did not say %q:\n%s", variant, frag, strings.Join(msgs, "\n"))
		}
	}
}

func TestCheckDecoderCatchesADecoderThatLeavesPartsOpen(t *testing.T) {
	mustFail(t, "leave-parts-open", func(tb testing.TB) {
		conformance.CheckDecoder(tb, echo{variant: "leave-parts-open"}, "testdata/echo.frames.json")
	})
}

func TestCheckDecoderCatchesAGoldenMismatch(t *testing.T) {
	dir := t.TempDir()
	raw, _ := os.ReadFile("testdata/echo.frames.json")                        //nolint:gosec // test fixture
	if err := os.WriteFile(dir+"/echo.frames.json", raw, 0o600); err != nil { //nolint:gosec // a temp dir
		t.Fatal(err)
	}
	// no golden file: a hard failure with instructions
	mustFail(t, "missing golden", func(tb testing.TB) { conformance.CheckDecoder(tb, echo{}, dir+"/echo.frames.json") })
	// write it, then damage it
	t.Setenv(conformance.UpdateEnv, "1")
	conformance.CheckDecoderDir(t, echo{}, dir)
	t.Setenv(conformance.UpdateEnv, "")
	conformance.CheckDecoderDir(t, echo{}, dir)                                                                                   // now passes against its own golden
	g, _ := os.ReadFile(dir + "/echo.golden.json")                                                                                //nolint:gosec // test file in a temp dir
	if err := os.WriteFile(dir+"/echo.golden.json", []byte(strings.Replace(string(g), "Hello", "Howdy", 1)), 0o600); err != nil { //nolint:gosec // a temp dir
		t.Fatal(err)
	}
	mustFail(t, "damaged golden", func(tb testing.TB) { conformance.CheckDecoder(tb, echo{}, dir+"/echo.frames.json") })
}

func TestLoadFixtureDataAndDataText(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/x.frames.json"
	body := `{"frames":[{"event":"a","id":"1","delay_ms":7,"data":{ "k" : 1 }},{"data_text":"[DONE]"}]}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	fx, err := conformance.LoadFixture(p)
	if err != nil {
		t.Fatal(err)
	}
	if fx.Name != "x" || len(fx.Frames) != 2 {
		t.Fatalf("fx = %+v", fx)
	}
	f0, f1 := fx.Frames[0], fx.Frames[1]
	if f0.Frame.Event != "a" || f0.Frame.ID != "1" || string(f0.Frame.Data) != `{"k":1}` || f0.Delay != 7*time.Millisecond {
		t.Errorf("frame 0 = %+v", f0)
	}
	if string(f1.Frame.Data) != "[DONE]" {
		t.Errorf("frame 1 = %+v", f1)
	}
	if err := os.WriteFile(p, []byte(`{"frames":[{"data":{},"data_text":"x"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := conformance.LoadFixture(p); err == nil {
		t.Error("a frame with both data and data_text must be rejected")
	}
}

// A tool_result that answers an earlier tool_call part passes, whether or not
// the call has closed.
func TestValidateAcceptsCorrelatedToolResult(t *testing.T) {
	call := e(chatstream.VerbPartStart, func(x *chatstream.Event) {
		x.PartID, x.Kind = "c", "tool_call"
		x.Meta = map[string]json.RawMessage{chatstream.MetaName: json.RawMessage(`"get"`)}
	})
	res := e(chatstream.VerbPartStart, func(x *chatstream.Event) {
		x.PartID, x.Kind = "r", "tool_result"
		x.Meta = map[string]json.RawMessage{chatstream.MetaCallID: json.RawMessage(`"c"`), chatstream.MetaIsError: json.RawMessage(`true`)}
	})
	evs := []chatstream.Event{e(chatstream.VerbRunStart, nil), call,
		e(chatstream.VerbPartEnd, func(x *chatstream.Event) { x.PartID = "c" }), res,
		e(chatstream.VerbPartEnd, func(x *chatstream.Event) { x.PartID = "r" }),
		e(chatstream.VerbRunFinish, func(x *chatstream.Event) { x.Reason = "stop" })}
	if v := conformance.Validate(evs); len(v) != 0 {
		t.Fatalf("violations: %v", v)
	}
	if !evs[3].MetaBool(chatstream.MetaIsError) || evs[3].MetaString(chatstream.MetaCallID) != "c" {
		t.Error("Meta accessors")
	}
}
