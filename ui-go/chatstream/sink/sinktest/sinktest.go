package sinktest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui-go/chatstream/sink"
	ssekit "github.com/hollis-labs/libs/ui-go/ssekit"
)

// UpdateEnv is the environment variable that, when set, makes CheckGolden rewrite golden files instead of
// comparing. Review the diff before committing.
const UpdateEnv = "CHATSTREAM_UPDATE_GOLDEN"

// T0 is the clock scenarios use.
var T0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// Scenario is a run's events, in order, with seq 1..n assigned. Cause is the
// error Close is called with; when the scenario has no terminal event it is why
// the stream was cut short.
type Scenario struct {
	Name   string
	Events []chatstream.Event
	// Truncated is true for a scenario with no terminal event: Close does the
	// signaling.
	Truncated bool
	Cause     error
}

// ErrCut is the cause the truncated scenarios pass to Close.
var ErrCut = errors.New("connection reset by peer")

type builder struct {
	evs []chatstream.Event
}

func (b *builder) add(verb chatstream.Verb, f func(*chatstream.Event)) {
	ev := chatstream.Event{V: chatstream.SchemaVersion, Seq: uint64(len(b.evs) + 1), RunID: "run-1", Time: T0, Verb: verb}
	if f != nil {
		f(&ev)
	}
	b.evs = append(b.evs, ev)
}

func meta(kv ...any) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	for i := 0; i+1 < len(kv); i += 2 {
		raw, _ := json.Marshal(kv[i+1])
		m[kv[i].(string)] = raw
	}
	return m
}

func (b *builder) start() {
	b.add(chatstream.VerbRunStart, func(e *chatstream.Event) { e.Provider, e.Model = "anthropic", "claude-x" })
	b.add(chatstream.VerbMessageStart, func(e *chatstream.Event) { e.MessageID, e.Role = "msg-1", "assistant" })
}

func (b *builder) part(id string, kind chatstream.PartKind, m map[string]json.RawMessage) {
	b.add(chatstream.VerbPartStart, func(e *chatstream.Event) { e.PartID, e.Kind, e.Meta = id, string(kind), m })
}

func (b *builder) text(id, s string) {
	b.add(chatstream.VerbPartDelta, func(e *chatstream.Event) { e.PartID, e.Text = id, s })
}

func (b *builder) frag(id, s string) {
	b.add(chatstream.VerbPartDelta, func(e *chatstream.Event) { e.PartID, e.JSONFragment = id, s })
}

func (b *builder) end(id string, final string) {
	b.add(chatstream.VerbPartEnd, func(e *chatstream.Event) {
		e.PartID = id
		if final != "" {
			e.Final = json.RawMessage(final)
		}
	})
}

func (b *builder) finish(reason chatstream.FinishReason, raw string, u *chatstream.Usage) {
	b.add(chatstream.VerbMessageEnd, nil)
	b.add(chatstream.VerbRunFinish, func(e *chatstream.Event) { e.Reason, e.RawReason, e.Usage = string(reason), raw, u })
}

func (b *builder) approval(id, call string, mode chatstream.ApprovalMode, desc string) {
	b.add(chatstream.VerbApprovalRequest, func(e *chatstream.Event) {
		e.ApprovalID, e.CallID, e.Mode, e.Reason = id, call, mode, "runs a shell command"
		e.Descriptor = json.RawMessage(desc)
		if mode == chatstream.ApprovalSuspend {
			exp := T0.Add(5 * time.Minute)
			e.ExpiresAt = &exp
		}
	})
}

const bashDescriptor = `{"tool":"bash","input":{"cmd":"ls"}}`

// Scenarios returns the canonical set. Names are stable: golden files are keyed
// on them.
func Scenarios() []Scenario {
	var out []Scenario
	add := func(name string, f func(b *builder)) {
		b := &builder{}
		f(b)
		out = append(out, Scenario{Name: name, Events: b.evs})
	}
	cut := func(name string, f func(b *builder)) {
		b := &builder{}
		f(b)
		out = append(out, Scenario{Name: name, Events: b.evs, Truncated: true, Cause: ErrCut})
	}

	add("plain_text", func(b *builder) {
		b.start()
		b.part("t1", chatstream.PartText, nil)
		b.text("t1", "Hello")
		b.text("t1", ", world")
		b.end("t1", "")
		b.finish(chatstream.FinishStop, "end_turn", nil)
	})
	add("reasoning_then_text", func(b *builder) {
		b.start()
		b.part("r1", chatstream.PartReasoning, nil)
		b.text("r1", "let me think ")
		b.text("r1", "about it")
		b.end("r1", `"sig-abc"`)
		b.part("t1", chatstream.PartText, nil)
		b.text("t1", "Answer.")
		b.end("t1", "")
		b.finish(chatstream.FinishStop, "stop", nil)
	})
	add("tool_call_and_result", func(b *builder) {
		b.start()
		b.add(chatstream.VerbStepStart, func(e *chatstream.Event) { e.StepID, e.Name = "s1", "call" })
		b.part("t1", chatstream.PartText, nil)
		b.text("t1", "Checking. ")
		b.end("t1", "")
		b.part("c1", chatstream.PartToolCall, meta(sink.MetaName, "get_weather", sink.MetaDetail, "Oslo"))
		b.frag("c1", `{"city":`)
		b.frag("c1", `"Oslo"}`)
		b.end("c1", `{"city":"Oslo"}`)
		b.part("res1", chatstream.PartToolResult, meta(sink.MetaCallID, "c1", sink.MetaName, "get_weather"))
		b.text("res1", "sunny, 21C")
		b.end("res1", "")
		b.add(chatstream.VerbStepFinish, func(e *chatstream.Event) { e.StepID = "s1" })
		b.finish(chatstream.FinishToolCalls, "tool_use", nil)
	})
	add("tool_result_error_and_whole_args", func(b *builder) {
		b.start()
		b.part("c1", chatstream.PartToolCall, meta(sink.MetaName, "bash"))
		b.end("c1", `{"cmd":"false"}`) // arguments arrive whole, no fragments
		b.part("res1", chatstream.PartToolResult, meta(sink.MetaCallID, "c1", sink.MetaName, "bash", sink.MetaIsError, true))
		b.text("res1", "exit status 1")
		b.end("res1", "")
		b.finish(chatstream.FinishToolCalls, "", nil)
	})
	add("approval_inband_after_call", func(b *builder) {
		b.start()
		b.part("c1", chatstream.PartToolCall, meta(sink.MetaName, "bash"))
		b.frag("c1", `{"cmd":"ls"}`)
		b.end("c1", "")
		b.approval("ap-1", "c1", chatstream.ApprovalInBand, bashDescriptor)
		b.finish(chatstream.FinishToolCalls, "", nil)
	})
	add("approval_inband_before_call", func(b *builder) {
		b.start()
		b.approval("ap-1", "c1", chatstream.ApprovalInBand, bashDescriptor)
		b.part("c1", chatstream.PartToolCall, meta(sink.MetaName, "bash"))
		b.frag("c1", `{"cmd":"ls"}`)
		b.end("c1", "")
		b.finish(chatstream.FinishToolCalls, "", nil)
	})
	add("approval_inband_no_call_id", func(b *builder) {
		b.start()
		b.approval("ap-9", "", chatstream.ApprovalInBand, bashDescriptor)
		b.part("c7", chatstream.PartToolCall, meta(sink.MetaName, "bash"))
		b.end("c7", `{"cmd":"ls"}`)
		b.finish(chatstream.FinishToolCalls, "", nil)
	})
	add("approval_suspend", func(b *builder) {
		b.start()
		b.part("c1", chatstream.PartToolCall, meta(sink.MetaName, "bash"))
		b.end("c1", `{"cmd":"rm -rf x"}`)
		b.approval("ap-2", "c1", chatstream.ApprovalSuspend, bashDescriptor)
		b.finish(chatstream.FinishStop, "", nil)
	})
	add("error_terminal", func(b *builder) {
		b.start()
		b.part("t1", chatstream.PartText, nil)
		b.text("t1", "partial")
		b.end("t1", "")
		b.add(chatstream.VerbRunError, func(e *chatstream.Event) {
			e.Code, e.Message, e.Retryable = "rate_limited", "slow down", true
		})
	})
	add("abort_terminal", func(b *builder) {
		b.start()
		b.part("t1", chatstream.PartText, nil)
		b.text("t1", "partial")
		b.end("t1", "")
		b.add(chatstream.VerbRunAbort, func(e *chatstream.Event) { e.Reason = "user canceled" })
	})
	add("usage_with_cache_and_reasoning", func(b *builder) {
		b.start()
		b.add(chatstream.VerbUsage, func(e *chatstream.Event) {
			e.Usage = &chatstream.Usage{Scope: chatstream.UsageCumulative, UncachedInput: 400, CacheRead: 600}
		})
		b.part("r1", chatstream.PartReasoning, nil)
		b.text("r1", "hmm")
		b.end("r1", "")
		b.part("t1", chatstream.PartText, nil)
		b.text("t1", "done")
		b.end("t1", "")
		b.finish(chatstream.FinishLength, "max_tokens", &chatstream.Usage{
			Scope: chatstream.UsageFinal, UncachedInput: 400, CacheRead: 600, CacheWrite: 50, Output: 150, Reasoning: 50})
	})
	add("gap_in_stream", func(b *builder) {
		b.start()
		b.part("t1", chatstream.PartText, nil)
		b.text("t1", "before")
		b.add(chatstream.VerbGap, func(e *chatstream.Event) { e.From, e.To, e.Reason = 5, 9, chatstream.GapRetention })
		b.text("t1", "after")
		b.end("t1", "")
		b.finish(chatstream.FinishStop, "", nil)
	})
	add("escaping_and_unicode", func(b *builder) {
		b.start()
		b.part("t1", chatstream.PartText, nil)
		b.text("t1", "line one\nline two\r\nquote \" backslash \\   tab\t")
		b.text("t1", "data: not a frame\n\nevent: nope")
		b.text("t1", "héllo 世界 🌍 <script>&")
		b.end("t1", "")
		b.finish(chatstream.FinishStop, "", nil)
	})
	add("raw_and_activity", func(b *builder) {
		b.start()
		b.add(chatstream.VerbRaw, func(e *chatstream.Event) {
			e.Raw = &chatstream.Raw{Dialect: "nanite", Type: "slot_changed", Payload: json.RawMessage(`{"type":"slot_changed","content":"slot 2"}`)}
		})
		b.add(chatstream.VerbRaw, func(e *chatstream.Event) {
			e.Raw = &chatstream.Raw{Dialect: "anthropic", Type: "ping", Payload: json.RawMessage(`{"type":"ping"}`)}
		})
		b.add(chatstream.VerbActivity, func(e *chatstream.Event) {
			e.Kind, e.Value = "nanite.status", json.RawMessage(`{"text":"compacting"}`)
		})
		b.add(chatstream.VerbActivity, func(e *chatstream.Event) {
			e.Kind, e.Patch = "nanite.status", json.RawMessage(`[{"op":"replace","path":"/text","value":"done"}]`)
		})
		b.add(chatstream.VerbActivity, func(e *chatstream.Event) {
			e.Kind, e.Value = sink.ActivityStateSnapshot, json.RawMessage(`{"todo":[1,2]}`)
		})
		b.add(chatstream.VerbActivity, func(e *chatstream.Event) {
			e.Kind, e.Value = sink.ActivityReplaceContent, json.RawMessage(`{"content":"replaced"}`)
		})
		b.finish(chatstream.FinishStop, "", nil)
	})
	add("source_file_data_parts", func(b *builder) {
		b.start()
		b.part("s1", chatstream.PartSource, meta(sink.MetaURL, "https://example.com/a", sink.MetaTitle, "A"))
		b.end("s1", "")
		b.part("f1", chatstream.PartFile, meta(sink.MetaURL, "data:image/png;base64,AAAA", sink.MetaMediaType, "image/png"))
		b.end("f1", "")
		b.part("d1", chatstream.PartData, meta(sink.MetaDataName, "weather"))
		b.end("d1", `{"temp":21}`)
		b.part("x1", chatstream.PartRefusal, nil)
		b.text("x1", "I can't help with that.")
		b.end("x1", "")
		b.finish(chatstream.FinishRefusal, "refusal", nil)
	})
	cut("truncated_mid_text", func(b *builder) {
		b.start()
		b.part("t1", chatstream.PartText, nil)
		b.text("t1", "half a sen")
	})
	cut("truncated_before_anything", func(b *builder) {})
	return out
}

// Named returns the named scenario.
func Named(t testing.TB, name string) Scenario {
	t.Helper()
	for _, s := range Scenarios() {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no scenario %q", name)
	return Scenario{}
}

// Recorder is a Writer that keeps what was written and counts flushes.
type Recorder struct {
	bytes.Buffer
	Flushes int
}

// Flush counts a flush.
func (r *Recorder) Flush() error { r.Flushes++; return nil }

// Failing is a Writer that fails: its Nth Write (1-based, 0 = never) and Nth
// Flush return Err. After a Write fails every later Write fails, and likewise
// for Flush, like a dead connection; the two are independent so a test can tell
// whether an encoder honors write errors and flush errors separately.
type Failing struct {
	Recorder
	FailWrite, FailFlush int
	Err                  error
	writes, flushes      int
	writeDead, flushDead bool
}

// Write fails on the FailWrite'th call and after.
func (f *Failing) Write(p []byte) (int, error) {
	f.writes++
	if f.writeDead || (f.FailWrite > 0 && f.writes >= f.FailWrite) {
		f.writeDead = true
		return 0, f.Err
	}
	return f.Recorder.Write(p)
}

// Flush fails on the FailFlush'th call and after.
func (f *Failing) Flush() error {
	f.flushes++
	if f.flushDead || (f.FailFlush > 0 && f.flushes >= f.FailFlush) {
		f.flushDead = true
		return f.Err
	}
	return f.Recorder.Flush()
}

// Frame is one parsed SSE frame.
type Frame struct {
	ID    string
	Event string
	Data  string
}

// Parse reads encoded output back into frames with go-ssekit's parser, which is
// the point: it fails when an encoder's bytes are not valid SSE, or when text
// containing newlines or "data:" leaked into the framing.
func Parse(t testing.TB, out []byte) []Frame {
	t.Helper()
	var frames []Frame
	for ev, err := range ssekit.Read(bytes.NewReader(out)) {
		if err != nil {
			t.Fatalf("output is not valid SSE: %v\n%s", err, out)
		}
		frames = append(frames, Frame{ID: ev.ID, Event: ev.Name, Data: string(ev.Data)})
	}
	return frames
}

// Play encodes every event of sc, then closes, on a fresh Recorder, and returns
// the parsed frames. The error is the first Encode or Close error.
func Play(t testing.TB, enc sink.Encoder, sc Scenario) []Frame {
	t.Helper()
	rec := &Recorder{}
	for i, ev := range sc.Events {
		if err := enc.Encode(rec, ev); err != nil {
			t.Fatalf("%s/%s: Encode %d (%s): %v", enc.Name(), sc.Name, i, ev.Verb, err)
		}
	}
	var cause error
	if sc.Truncated {
		cause = sc.Cause
	}
	if err := enc.Close(rec, cause); err != nil {
		t.Fatalf("%s/%s: Close: %v", enc.Name(), sc.Name, err)
	}
	if rec.Flushes == 0 && rec.Len() > 0 {
		t.Errorf("%s/%s: wrote %d bytes and never flushed", enc.Name(), sc.Name, rec.Len())
	}
	return Parse(t, rec.Bytes())
}

// Render formats frames one per line for golden files: `id=<id> event=<name> <data>`
// with the fields present.
func Render(frames []Frame) string {
	var b strings.Builder
	for _, f := range frames {
		var parts []string
		if f.ID != "" {
			parts = append(parts, "id="+f.ID)
		}
		if f.Event != "" {
			parts = append(parts, "event="+f.Event)
		}
		parts = append(parts, f.Data)
		b.WriteString(strings.Join(parts, " "))
		b.WriteByte('\n')
	}
	return b.String()
}

// CheckGolden compares got with testdata/<name>.golden in dir, or rewrites it
// under UpdateEnv.
func CheckGolden(t testing.TB, dir, name, got string) {
	t.Helper()
	path := filepath.Join(dir, "testdata", name+".golden")
	if os.Getenv(UpdateEnv) != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("no golden file %s (%v); run with %s=1 to create it", path, err, UpdateEnv)
	}
	if string(want) != got {
		t.Errorf("%s differs from %s\n--- got ---\n%s--- want ---\n%s", name, path, got, want)
	}
}

// Types checks the Writer implementations satisfy sink.Writer.
var (
	_ sink.Writer = (*Recorder)(nil)
	_ sink.Writer = (*Failing)(nil)
)

// MustJSON unmarshals a frame's data into a map, failing the test if it is not
// a JSON object.
func MustJSON(t testing.TB, f Frame) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(f.Data), &m); err != nil {
		t.Fatalf("frame data is not a JSON object: %v: %s", err, f.Data)
	}
	return m
}

// Describe is a one-line summary for failure messages.
func Describe(frames []Frame) string {
	var kinds []string
	for _, f := range frames {
		kinds = append(kinds, fmt.Sprintf("%q", f.Data))
	}
	return strings.Join(kinds, ", ")
}

// JoinedText concatenates, in order, string field `field` of every frame whose
// data is a JSON object with `typeField` equal to one of types. It is how the
// escaping tests check text survives a round trip through the framing.
func JoinedText(t testing.TB, frames []Frame, typeField string, types []string, field string) string {
	t.Helper()
	var b strings.Builder
	for _, f := range frames {
		var m map[string]any
		if json.Unmarshal([]byte(f.Data), &m) != nil {
			continue
		}
		ty, _ := m[typeField].(string)
		for _, want := range types {
			if ty == want {
				s, _ := m[field].(string)
				b.WriteString(s)
			}
		}
	}
	return b.String()
}

// EscapingText is the text the escaping_and_unicode scenario streams.
func EscapingText() string {
	return "line one\nline two\r\nquote \" backslash \\   tab\t" + "data: not a frame\n\nevent: nope" + "héllo 世界 🌍 <script>&"
}

// Builder assembles a custom scenario for one test. Events get seq 1..n, run id
// "run-1" and the fixed clock, like the canonical scenarios.
type Builder struct{ b builder }

// Events returns what was built.
func (b *Builder) Events() []chatstream.Event { return b.b.evs }

// Meta builds a part.start Meta map from key, value pairs.
func Meta(kv ...any) map[string]json.RawMessage { return meta(kv...) }

// Start adds run.start and message.start.
func (b *Builder) Start() { b.b.start() }

// Part adds a part.start; Text and Frag add deltas; End a part.end.
func (b *Builder) Part(id string, kind chatstream.PartKind, m map[string]json.RawMessage) {
	b.b.part(id, kind, m)
}
func (b *Builder) Text(id, s string)    { b.b.text(id, s) }
func (b *Builder) Frag(id, s string)    { b.b.frag(id, s) }
func (b *Builder) End(id, final string) { b.b.end(id, final) }

// Finish adds message.end and run.finish.
func (b *Builder) Finish(reason chatstream.FinishReason, raw string, u *chatstream.Usage) {
	b.b.finish(reason, raw, u)
}

// Approval adds an approval.request.
func (b *Builder) Approval(id, call string, mode chatstream.ApprovalMode, descriptor string) {
	b.b.approval(id, call, mode, descriptor)
}

// UsageDelta adds a usage event carrying u.
func (b *Builder) UsageDelta(u chatstream.Usage) {
	b.b.add(chatstream.VerbUsage, func(e *chatstream.Event) { e.Usage = &u })
}

// Activity adds an activity event of kind with a JSON value.
func (b *Builder) Activity(kind, value string) {
	b.b.add(chatstream.VerbActivity, func(e *chatstream.Event) { e.Kind, e.Value = kind, json.RawMessage(value) })
}

// Add adds any event.
func (b *Builder) Add(verb chatstream.Verb, f func(*chatstream.Event)) { b.b.add(verb, f) }

// Build runs f on a fresh Builder and returns its events.
func Build(f func(b *Builder)) []chatstream.Event {
	var b Builder
	f(&b)
	return b.Events()
}
