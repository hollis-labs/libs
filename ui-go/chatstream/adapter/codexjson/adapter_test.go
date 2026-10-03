package codexjson_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui-go/chatstream/adapter/codexjson"
	"github.com/hollis-labs/libs/ui-go/chatstream/conformance"
)

func TestFixtures(t *testing.T) {
	conformance.CheckDecoderDir(t, codexjson.New(), "testdata")
}

func TestTruncationAtEveryFrameBoundary(t *testing.T) {
	paths, _ := filepath.Glob("testdata/*.frames.json")
	if len(paths) == 0 {
		t.Fatal("no fixtures")
	}
	for _, p := range paths {
		fx, err := conformance.LoadFixture(p)
		if err != nil {
			t.Fatal(err)
		}
		conformance.CheckTruncation(t, codexjson.New(), fx)
	}
}

func TestAdapterMetadata(t *testing.T) {
	a := codexjson.New()
	if a.Name() != "codex.exec-json" || a.Framing() != chatstream.FramingNDJSON {
		t.Errorf("name %q framing %v", a.Name(), a.Framing())
	}
	if err := a.Capabilities().Validate(); err != nil {
		t.Errorf("capabilities: %v", err)
	}
	c := a.Capabilities()
	if c.Text != chatstream.GranularityFinal || c.ToolArgs != chatstream.GranularityFinal || c.Approval != chatstream.ApprovalNone ||
		c.Cancel || c.ResumeCursor || c.Reasoning != chatstream.ReasoningSummary || c.ReasoningRoundTrip || !c.CacheTokens || !c.ReasoningTokens {
		t.Errorf("capabilities over- or under-claim: %+v", c)
	}
}

func decode(t *testing.T, a chatstream.Adapter, lines ...string) []chatstream.Event {
	t.Helper()
	d := a.NewDecoder(conformance.DecodeOptions())
	var out []chatstream.Event
	for _, l := range lines {
		evs, err := d.Decode(chatstream.Frame{Data: []byte(l)})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, evs...)
	}
	return append(out, d.Close(nil)...)
}

func run(t *testing.T, lines ...string) []chatstream.Event {
	t.Helper()
	return decode(t, codexjson.New(), lines...)
}

func last(evs []chatstream.Event) chatstream.Event { return evs[len(evs)-1] }

func verbs(evs []chatstream.Event) []string {
	var out []string
	for _, e := range evs {
		out = append(out, string(e.Verb))
	}
	return out
}

func TestTurnEndingLines(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		verb  chatstream.Verb
		code  string
		msg   string
		extra string
	}{
		{"completed", `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`, chatstream.VerbRunFinish, "", "", ""},
		{"completed without usage", `{"type":"turn.completed"}`, chatstream.VerbRunFinish, "", "", ""},
		{"failed", `{"type":"turn.failed","error":{"message":"boom"}}`, chatstream.VerbRunError, "turn_failed", "boom", ""},
		{"failed without message", `{"type":"turn.failed"}`, chatstream.VerbRunError, "turn_failed", "", ""},
		{"failed with error as a string", `{"type":"turn.failed","error":"boom"}`, chatstream.VerbRunError, "turn_failed", "boom", ""},
		{"failed with a structured error", `{"type":"turn.failed","error":{"message":{"a":1}}}`, chatstream.VerbRunError, "turn_failed", `{"message":{"a":1}}`, ""},
		{"completed with float token counts", `{"type":"turn.completed","usage":{"input_tokens":1.0,"output_tokens":1}}`, chatstream.VerbRunFinish, "", "", ""},
		{"stream error with a structured message", `{"type":"error","message":{"text":"gone"}}`, chatstream.VerbRunError, chatstream.CodeUpstreamError, "gone", ""},
		{"stream error", `{"type":"error","message":"gone"}`, chatstream.VerbRunError, chatstream.CodeUpstreamError, "gone", ""},
	}
	for _, tc := range tests {
		evs := run(t, `{"type":"thread.started","thread_id":"t"}`, `{"type":"turn.started"}`, tc.line, `{"type":"item.completed","item":{"id":"late","type":"agent_message","text":"ignored"}}`)
		term := last(evs)
		if term.Verb != tc.verb || term.Code != tc.code || term.Message != tc.msg {
			t.Errorf("%s: terminal = %+v", tc.name, term)
		}
		if tc.verb == chatstream.VerbRunFinish && (term.Finish() != chatstream.FinishStop) {
			t.Errorf("%s: reason %q; the format has no finish reason, a completed turn is stop", tc.name, term.Reason)
		}
		n := 0
		for _, e := range evs {
			if e.IsTerminal() {
				n++
			}
			if e.Verb == chatstream.VerbPartStart && e.PartID == "late" {
				t.Errorf("%s: a line after the terminal event was decoded", tc.name)
			}
		}
		if n != 1 {
			t.Errorf("%s: %d terminal events", tc.name, n)
		}
		conformance.Check(t, evs)
	}
}

// An item error is a non-fatal item: it does not end the run.
func TestItemErrorIsNotTerminal(t *testing.T) {
	evs := run(t,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"e","type":"error","message":"soft failure"}}`,
		`{"type":"item.completed","item":{"id":"a","type":"agent_message","text":"carried on"}}`,
		`{"type":"turn.completed"}`)
	var act chatstream.Event
	for _, e := range evs {
		if e.Verb == chatstream.VerbActivity {
			act = e
		}
	}
	if act.Kind != "codex.error" || !strings.Contains(string(act.Value), "soft failure") || act.Raw == nil || act.Raw.Dialect != "codex" {
		t.Errorf("activity = %+v", act)
	}
	if last(evs).Verb != chatstream.VerbRunFinish {
		t.Errorf("verbs = %v", verbs(evs))
	}
	m, err := chatstream.Reduce(evs, nil)
	if err != nil || m.Text() != "carried on" || m.Status != chatstream.StatusFinished {
		t.Errorf("message = %+v, %v", m, err)
	}
}

func TestToolItemMappingTable(t *testing.T) {
	tests := []struct {
		name    string
		started string
		done    string
		tool    string
		args    string
		isError bool
		result  string
	}{
		{"command ok",
			`{"id":"i","type":"command_execution","command":"ls","status":"in_progress"}`,
			`{"id":"i","type":"command_execution","command":"ls","aggregated_output":"x","exit_code":0,"status":"completed"}`,
			"shell", `{"command":"ls"}`, false, `"exit_code":0`},
		{"command nonzero exit",
			`{"id":"i","type":"command_execution","command":"false","status":"in_progress"}`,
			`{"id":"i","type":"command_execution","command":"false","aggregated_output":"","exit_code":1,"status":"completed"}`,
			"shell", `{"command":"false"}`, true, `"exit_code":1`},
		{"command declined",
			`{"id":"i","type":"command_execution","command":"rm","status":"in_progress"}`,
			`{"id":"i","type":"command_execution","command":"rm","exit_code":null,"status":"declined"}`,
			"shell", `{"command":"rm"}`, true, `"status":"declined"`},
		{"command failed",
			`{"id":"i","type":"command_execution","command":"x","status":"in_progress"}`,
			`{"id":"i","type":"command_execution","command":"x","exit_code":null,"status":"failed"}`,
			"shell", `{"command":"x"}`, true, `"status":"failed"`},
		{"mcp ok",
			`{"id":"i","type":"mcp_tool_call","server":"s","tool":"t","arguments":{"a":1},"status":"in_progress"}`,
			`{"id":"i","type":"mcp_tool_call","server":"s","tool":"t","arguments":{"a":1},"result":{"content":[]},"status":"completed"}`,
			"t", `{"arguments":{"a":1},"server":"s","tool":"t"}`, false, `"result":{"content":[]}`},
		{"mcp error",
			`{"id":"i","type":"mcp_tool_call","server":"s","tool":"t","arguments":{},"status":"in_progress"}`,
			`{"id":"i","type":"mcp_tool_call","server":"s","tool":"t","arguments":{},"error":{"message":"no"},"status":"completed"}`,
			"t", `{"arguments":{},"server":"s","tool":"t"}`, true, `"error":{"message":"no"}`},
		{"file change ok", ``,
			`{"id":"i","type":"file_change","changes":[{"path":"a","kind":"add"}],"status":"completed"}`,
			"apply_patch", `{"changes":[{"path":"a","kind":"add"}]}`, false, `"changes"`},
		{"file change failed", ``,
			`{"id":"i","type":"file_change","changes":[],"status":"failed"}`,
			"apply_patch", `{"changes":[]}`, true, `"status":"failed"`},
		{"web search",
			`{"id":"i","type":"web_search","query":"q","status":"in_progress"}`,
			`{"id":"i","type":"web_search","query":"q","results":[{"u":1}],"status":"completed"}`,
			"web_search", `{"action":null,"query":"q"}`, false, `"results":[{"u":1}]`},
		{"collab",
			`{"id":"i","type":"collab_tool_call","tool":"spawn_agent","sender_thread_id":"s","receiver_thread_ids":["r"],"status":"in_progress"}`,
			`{"id":"i","type":"collab_tool_call","tool":"spawn_agent","sender_thread_id":"s","receiver_thread_ids":["r"],"agents_states":{"r":{"status":"running"}},"status":"failed"}`,
			"spawn_agent", `"receiver_thread_ids":["r"]`, true, `"agents_states"`},
	}
	for _, tc := range tests {
		lines := []string{`{"type":"turn.started"}`}
		if tc.started != "" {
			lines = append(lines, `{"type":"item.started","item":`+tc.started+`}`)
		}
		lines = append(lines, `{"type":"item.completed","item":`+tc.done+`}`, `{"type":"turn.completed"}`)
		evs := run(t, lines...)
		conformance.Check(t, evs)
		m, err := chatstream.Reduce(evs, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(m.Parts) != 2 {
			t.Errorf("%s: parts = %d, want call and result (a completed-only item gets both)", tc.name, len(m.Parts))
			continue
		}
		call, res := m.Parts[0], m.Parts[1]
		if call.Kind != chatstream.PartToolCall || !strings.Contains(string(call.Final), strings.Trim(tc.args, "{}")) {
			t.Errorf("%s: call = %+v", tc.name, call)
		}
		var name string
		_ = json.Unmarshal(call.Meta["name"], &name)
		if name != tc.tool {
			t.Errorf("%s: tool name %q, want %q", tc.name, name, tc.tool)
		}
		if res.Kind != chatstream.PartToolResult || res.ID != "i#result" || string(res.Meta["call_id"]) != `"i"` {
			t.Errorf("%s: result = %+v", tc.name, res)
		}
		if got := string(res.Meta["is_error"]) == "true"; got != tc.isError {
			t.Errorf("%s: is_error = %v, want %v", tc.name, got, tc.isError)
		}
		if !strings.Contains(string(res.Final), tc.result) {
			t.Errorf("%s: result final = %s, want it to contain %s", tc.name, res.Final, tc.result)
		}
	}
}

func TestToolItemUpdatedAndRepeatedCompletionAreRawNotDuplicates(t *testing.T) {
	evs := run(t,
		`{"type":"item.started","item":{"id":"c","type":"command_execution","command":"ls","status":"in_progress"}}`,
		`{"type":"item.updated","item":{"id":"c","type":"command_execution","command":"ls","status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"c","type":"command_execution","command":"ls","exit_code":0,"status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"c","type":"command_execution","command":"ls","exit_code":0,"status":"completed"}}`)
	calls, results, raws := 0, 0, 0
	for _, e := range evs {
		switch {
		case e.Verb == chatstream.VerbPartStart && e.Kind == "tool_call":
			calls++
		case e.Verb == chatstream.VerbPartStart && e.Kind == "tool_result":
			results++
		case e.Verb == chatstream.VerbRaw:
			raws++
		}
	}
	if calls != 1 || results != 1 || raws != 1 {
		t.Errorf("calls %d results %d raws %d in %v", calls, results, raws, verbs(evs))
	}
}

func TestTextGrowsAndRewritesAreHandled(t *testing.T) {
	evs := run(t,
		`{"type":"item.started","item":{"id":"a","type":"agent_message","text":"Hel"}}`,
		`{"type":"item.updated","item":{"id":"a","type":"agent_message","text":"Hello"}}`,
		`{"type":"item.updated","item":{"id":"a","type":"agent_message","text":"Jello"}}`,
		`{"type":"item.completed","item":{"id":"a","type":"agent_message","text":"Hello, world"}}`,
		`{"type":"item.completed","item":{"id":"a","type":"agent_message","text":"Hello, world"}}`)
	m, err := chatstream.Reduce(evs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Text() != "Hello, world" || len(m.Parts) != 1 {
		t.Errorf("parts = %+v", m.Parts)
	}
	raws := 0
	for _, e := range evs {
		if e.Verb == chatstream.VerbRaw {
			raws++
		}
	}
	if raws != 2 { // the rewrite and the repeated completion
		t.Errorf("%d raw events, want 2 in %v", raws, verbs(evs))
	}
	// an empty message never becomes a part
	evs = run(t, `{"type":"item.started","item":{"id":"b","type":"agent_message","text":""}}`, `{"type":"item.completed","item":{"id":"b","type":"agent_message","text":""}}`)
	for _, e := range evs {
		if e.Verb == chatstream.VerbPartStart {
			t.Error("an empty message produced a part")
		}
	}
}

func TestReasoningItemIsAReasoningPart(t *testing.T) {
	evs := run(t, `{"type":"item.completed","item":{"id":"r","type":"reasoning","text":"thinking"}}`)
	m, _ := chatstream.Reduce(evs, nil)
	if len(m.Parts) != 1 || m.Parts[0].Kind != chatstream.PartReasoning || m.Parts[0].Text != "thinking" || len(m.Parts[0].Final) != 0 {
		t.Errorf("parts = %+v (a summary has no signature to return)", m.Parts)
	}
}

// Codex's counts are inclusive. Summing input plus cache would count the cached
// tokens twice: the disjoint total must equal what was billed, input + output.
func TestUsageIsDisjointNeverDoubleCounted(t *testing.T) {
	evs := run(t, `{"type":"turn.completed","usage":{"input_tokens":24763,"cached_input_tokens":24448,"cache_write_input_tokens":0,"output_tokens":122,"reasoning_output_tokens":40}}`)
	u := last(evs).Usage
	if u == nil || u.Scope != chatstream.UsageFinal {
		t.Fatalf("usage = %+v", u)
	}
	if u.UncachedInput != 315 || u.CacheRead != 24448 || u.Output != 82 || u.Reasoning != 40 {
		t.Errorf("components = %+v", u)
	}
	if u.Total() != 24763+122 {
		t.Errorf("Total = %d, want input + output = %d", u.Total(), 24763+122)
	}
	if naive := 24763 + 24448 + 122; u.Total() == naive {
		t.Error("the total double-counts cached tokens")
	}
}

func TestInconsistentUsageIsNotReported(t *testing.T) {
	for _, usage := range []string{
		`{"input_tokens":10,"cached_input_tokens":50,"output_tokens":5}`,
		`{"input_tokens":10,"output_tokens":5,"reasoning_output_tokens":9}`,
	} {
		evs := run(t, `{"type":"turn.completed","usage":`+usage+`}`)
		term := last(evs)
		if term.Verb != chatstream.VerbRunFinish || term.Usage != nil {
			t.Errorf("%s: terminal = %+v", usage, term)
		}
		if !strings.Contains(string(term.Ext["codex"]), "usage_error") {
			t.Errorf("%s: the reason should be in Ext: %v", usage, term.Ext)
		}
		conformance.Check(t, evs)
	}
}

// A resumed thread reports cumulative counters; with the baseline the run.finish
// carries only this turn's change.
func TestCumulativeUsageBecomesTheTurnsDelta(t *testing.T) {
	turn1 := `{"type":"turn.completed","usage":{"input_tokens":1000,"cached_input_tokens":200,"output_tokens":100,"reasoning_output_tokens":10}}`
	turn2 := `{"type":"turn.completed","usage":{"input_tokens":2500,"cached_input_tokens":900,"output_tokens":260,"reasoning_output_tokens":30}}`

	first := decode(t, codexjson.New(codexjson.WithCumulativeUsage(chatstream.Usage{})), turn1)
	u1 := last(first).Usage
	if u1 == nil || u1.Total() != 1100 {
		t.Fatalf("turn 1 = %+v", u1)
	}
	// turn 2 continues the thread: baseline is everything turn 1 used
	second := decode(t, codexjson.New(codexjson.WithCumulativeUsage(*u1)), turn2)
	u2 := last(second).Usage
	if u2 == nil {
		t.Fatal("no usage for turn 2")
	}
	want := chatstream.Usage{Scope: chatstream.UsageFinal, UncachedInput: (2500 - 900) - (1000 - 200), CacheRead: 700, Output: (260 - 30) - (100 - 10), Reasoning: 20}
	if u2.UncachedInput != want.UncachedInput || u2.CacheRead != want.CacheRead || u2.Output != want.Output || u2.Reasoning != want.Reasoning || u2.Scope != chatstream.UsageFinal {
		t.Errorf("delta = %+v, want %+v", u2, want)
	}
	if u1.Total()+u2.Total() != 2500+260 {
		t.Errorf("the turns' totals %d + %d must add up to the thread's %d", u1.Total(), u2.Total(), 2500+260)
	}

	// without the option the counters are taken as this turn's own
	plain := last(decode(t, codexjson.New(), turn2)).Usage
	if plain.Total() != 2760 {
		t.Errorf("per-turn reading = %+v", plain)
	}
	// a counter that went backwards is not a delta
	back := decode(t, codexjson.New(codexjson.WithCumulativeUsage(*u1)), `{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":1}}`)
	if last(back).Usage != nil || !strings.Contains(string(last(back).Ext["codex"]), "usage_error") {
		t.Errorf("backwards counters: %+v", last(back))
	}
}

func TestMalformedAndUnknownAreRawNotTerminal(t *testing.T) {
	for _, l := range []string{`not json`, `{"foo":1}`, `[]`, `{"type":"future"}`, `{"type":"item.completed","item":{"type":"agent_message"}}`, `{"type":"item.completed"}`} {
		evs := run(t, `{"type":"thread.started","thread_id":"t"}`, l, `{"type":"turn.completed"}`)
		if evs[1].Verb != chatstream.VerbRaw {
			t.Errorf("%q: event 1 = %s", l, evs[1].Verb)
		}
		if last(evs).Verb != chatstream.VerbRunFinish {
			t.Errorf("%q: %v", l, verbs(evs))
		}
		conformance.Check(t, evs)
	}
}

func TestThreadIDBecomesTheRunIDOnlyWhenNoneIsConfigured(t *testing.T) {
	d := codexjson.New().NewDecoder(chatstream.DecodeOptions{Now: conformance.DecodeOptions().Now})
	evs, _ := d.Decode(chatstream.Frame{Data: []byte(`{"type":"thread.started","thread_id":"thr-1"}`)})
	if evs[0].RunID != "thr-1" {
		t.Errorf("run id = %q", evs[0].RunID)
	}
	evs = run(t, `{"type":"thread.started","thread_id":"thr-1"}`)
	if evs[0].RunID != conformance.FixtureRunID {
		t.Errorf("a configured run id must win, got %q", evs[0].RunID)
	}
}

func TestEOFBeforeTheTurnEndsIsTruncation(t *testing.T) {
	evs := run(t, `{"type":"thread.started","thread_id":"t"}`, `{"type":"turn.started"}`, `{"type":"item.started","item":{"id":"c","type":"command_execution","command":"ls","status":"in_progress"}}`)
	term := last(evs)
	if term.Verb != chatstream.VerbRunError || term.Code != chatstream.CodeUpstreamTruncated {
		t.Fatalf("terminal = %+v", term)
	}
	conformance.Check(t, evs)
}

func TestToolMetaConvention(t *testing.T) {
	paths, _ := filepath.Glob("testdata/*.frames.json")
	if len(paths) == 0 {
		t.Fatal("no fixtures")
	}
	for _, p := range paths {
		fx, err := conformance.LoadFixture(p)
		if err != nil {
			t.Fatal(err)
		}
		evs, err := conformance.DecodeFixture(codexjson.New(), fx)
		if err != nil {
			t.Fatal(err)
		}
		calls := map[string]bool{}
		for _, e := range evs {
			if e.Verb == chatstream.VerbPartStart && e.Kind == "tool_call" {
				calls[e.PartID] = true
				if e.MetaString(chatstream.MetaName) == "" {
					t.Errorf("%s: tool_call %s has no name", fx.Name, e.PartID)
				}
			}
			if e.Verb == chatstream.VerbPartStart && e.Kind == "tool_result" && !calls[e.MetaString(chatstream.MetaCallID)] {
				t.Errorf("%s: result %s call_id %q is not a tool_call part", fx.Name, e.PartID, e.MetaString(chatstream.MetaCallID))
			}
		}
	}
}

func TestToolNameAndDetail(t *testing.T) {
	tests := []struct{ item, name, detail string }{
		{`{"id":"i","type":"command_execution","command":"ls -la","status":"in_progress"}`, "shell", "ls -la"},
		{`{"id":"i","type":"mcp_tool_call","server":"docs","tool":"search","arguments":{},"status":"in_progress"}`, "search", ""},
		{`{"id":"i","type":"file_change","changes":[{"path":"src/a.go","kind":"update"}],"status":"in_progress"}`, "apply_patch", "src/a.go"},
		{`{"id":"i","type":"web_search","query":"go iter","status":"in_progress"}`, "web_search", "go iter"},
		{`{"id":"i","type":"collab_tool_call","tool":"spawn_agent","prompt":"do x","status":"in_progress"}`, "spawn_agent", "do x"},
	}
	for _, tc := range tests {
		evs := run(t, `{"type":"item.started","item":`+tc.item+`}`)
		var st chatstream.Event
		for _, e := range evs {
			if e.Verb == chatstream.VerbPartStart && e.Kind == "tool_call" {
				st = e
			}
		}
		if st.MetaString(chatstream.MetaName) != tc.name || st.MetaString(chatstream.MetaDetail) != tc.detail {
			t.Errorf("%s: name %q detail %q, want %q %q", tc.item, st.MetaString(chatstream.MetaName), st.MetaString(chatstream.MetaDetail), tc.name, tc.detail)
		}
		conformance.Check(t, evs)
	}
}
