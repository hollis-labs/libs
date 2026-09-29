package hooks_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	hooks "github.com/hollis-labs/go-hooks"
)

// inputTypes maps each event to a constructor for its *Input type. A missing
// entry fails TestEveryEventHasInputType, so a twelfth event cannot be added
// without a payload type.
var inputTypes = map[hooks.Event]func() any{
	hooks.EventSessionStart:      func() any { return new(hooks.SessionStartInput) },
	hooks.EventSessionEnd:        func() any { return new(hooks.SessionEndInput) },
	hooks.EventUserPromptSubmit:  func() any { return new(hooks.UserPromptSubmitInput) },
	hooks.EventPreToolUse:        func() any { return new(hooks.PreToolUseInput) },
	hooks.EventPostToolUse:       func() any { return new(hooks.PostToolUseInput) },
	hooks.EventPermissionRequest: func() any { return new(hooks.PermissionRequestInput) },
	hooks.EventPreCompact:        func() any { return new(hooks.PreCompactInput) },
	hooks.EventPostCompact:       func() any { return new(hooks.PostCompactInput) },
	hooks.EventSubagentStart:     func() any { return new(hooks.SubagentStartInput) },
	hooks.EventSubagentStop:      func() any { return new(hooks.SubagentStopInput) },
	hooks.EventStop:              func() any { return new(hooks.StopInput) },
}

func TestEventValid(t *testing.T) {
	evs := hooks.Events()
	if len(evs) != 11 {
		t.Fatalf("Events() has %d entries, want 11", len(evs))
	}
	for _, e := range evs {
		if !e.Valid() {
			t.Errorf("%q should be valid", e)
		}
	}
	for _, bad := range []hooks.Event{"", "Interrupt", "Setup", "Notification", "CwdChanged", "pretooluse", "PreToolUse "} {
		if bad.Valid() {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestEventsReturnsFreshSlice(t *testing.T) {
	a := hooks.Events()
	a[0] = "Mutated"
	if hooks.Events()[0] != hooks.EventSessionStart {
		t.Fatal("Events() aliases internal state")
	}
}

func TestUsesToolMatcher(t *testing.T) {
	tool := map[hooks.Event]bool{
		hooks.EventPreToolUse: true, hooks.EventPostToolUse: true, hooks.EventPermissionRequest: true,
	}
	for _, e := range hooks.Events() {
		if got := e.UsesToolMatcher(); got != tool[e] {
			t.Errorf("%s.UsesToolMatcher() = %v, want %v", e, got, tool[e])
		}
	}
}

func TestEveryEventHasInputType(t *testing.T) {
	for _, e := range hooks.Events() {
		if inputTypes[e] == nil {
			t.Errorf("no *Input type registered for %s", e)
		}
	}
}

func TestInputGoldenRoundTrip(t *testing.T) {
	for _, e := range hooks.Events() {
		t.Run(string(e), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "golden", string(e)+".json"))
			if err != nil {
				t.Fatal(err)
			}
			v := inputTypes[e]()
			if uerr := json.Unmarshal(raw, v); uerr != nil {
				t.Fatalf("decode: %v", uerr)
			}
			enc, err := json.Marshal(v)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			var want, got any
			if err := json.Unmarshal(raw, &want); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(enc, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(want, got) {
				t.Errorf("round trip changed the JSON\n want %s\n  got %s", raw, enc)
			}
			// The encoded form must be flat: hook_event_name at top level,
			// no nested payload key.
			m := got.(map[string]any)
			if m["hook_event_name"] != string(e) {
				t.Errorf("hook_event_name = %v, want %s", m["hook_event_name"], e)
			}
			if _, nested := m["CommonInput"]; nested {
				t.Error("CommonInput leaked as a nested key")
			}
		})
	}
}

func TestOutputDecode(t *testing.T) {
	var o hooks.Output
	in := `{"decision":"ask","reason":"r","additionalContext":"c","updatedInput":{"a":1},"systemMessage":"s","continue":false,"stopReason":"x","hookSpecificOutput":{"ignored":true}}`
	if err := json.Unmarshal([]byte(in), &o); err != nil {
		t.Fatal(err)
	}
	if o.Decision != hooks.DecisionAsk || o.Reason != "r" || o.AdditionalContext != "c" ||
		o.SystemMessage != "s" || o.StopReason != "x" || o.UpdatedInput["a"] != float64(1) {
		t.Errorf("decoded %+v", o)
	}
	if o.Continue == nil || *o.Continue {
		t.Errorf("Continue = %v, want pointer to false", o.Continue)
	}
	var zero hooks.Output
	if err := json.Unmarshal([]byte(`{}`), &zero); err != nil || zero.Continue != nil {
		t.Errorf("empty object: %+v %v", zero, err)
	}
}

func TestOutputValidate(t *testing.T) {
	for _, d := range []hooks.Decision{"", hooks.DecisionAllow, hooks.DecisionDeny, hooks.DecisionAsk} {
		if err := (hooks.Output{Decision: d}).Validate(); err != nil {
			t.Errorf("decision %q: %v", d, err)
		}
	}
	for _, d := range []hooks.Decision{"block", "Allow", "approve", " deny"} {
		if err := (hooks.Output{Decision: d}).Validate(); err == nil {
			t.Errorf("decision %q accepted", d)
		}
	}
}

func TestDecisionValuesPinned(t *testing.T) {
	// Pinned to go-permission's Decision values, string for string.
	if hooks.DecisionAllow != "allow" || hooks.DecisionDeny != "deny" || hooks.DecisionAsk != "ask" {
		t.Fatal("decision values drifted from go-permission")
	}
}

func asValidation(err error) (*hooks.ValidationError, bool) {
	var ve *hooks.ValidationError
	ok := errors.As(err, &ve)
	return ve, ok
}

func validHook() hooks.Hook {
	return hooks.Hook{
		Name:    "guard",
		Event:   hooks.EventPreToolUse,
		Kind:    hooks.KindCommand,
		Command: "/bin/true",
		Timeout: time.Second,
		OnError: hooks.OnErrorClosed,
	}
}

func TestHookValidate(t *testing.T) {
	if err := validHook().Validate(); err != nil {
		t.Fatalf("valid hook rejected: %v", err)
	}
	mcp := validHook()
	mcp.Kind, mcp.Command = hooks.KindMCPTool, ""
	mcp.MCPTool = hooks.MCPToolRef{Server: "s", Tool: "t"}
	if err := mcp.Validate(); err != nil {
		t.Fatalf("valid mcp_tool hook rejected: %v", err)
	}

	cases := []struct {
		name  string
		mut   func(*hooks.Hook)
		field string
	}{
		{"empty name", func(h *hooks.Hook) { h.Name = "" }, "Name"},
		{"empty event", func(h *hooks.Hook) { h.Event = "" }, "Event"},
		{"unknown event", func(h *hooks.Hook) { h.Event = "Interrupt" }, "Event"},
		{"empty kind", func(h *hooks.Hook) { h.Kind = "" }, "Kind"},
		{"http kind", func(h *hooks.Hook) { h.Kind = "http" }, "Kind"},
		{"command without Command", func(h *hooks.Hook) { h.Command = "" }, "Command"},
		{"zero timeout", func(h *hooks.Hook) { h.Timeout = 0 }, "Timeout"},
		{"negative timeout", func(h *hooks.Hook) { h.Timeout = -time.Second }, "Timeout"},
		{"empty OnError", func(h *hooks.Hook) { h.OnError = "" }, "OnError"},
		{"unknown OnError", func(h *hooks.Hook) { h.OnError = "ignore" }, "OnError"},
		{"negative limit", func(h *hooks.Hook) { h.AdditionalContextLimit = -1 }, "AdditionalContextLimit"},
		{"bad layer", func(h *hooks.Hook) { h.Layer = "local" }, "Layer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := validHook()
			tc.mut(&h)
			err := h.Validate()
			ve, ok := asValidation(err)
			if !ok {
				t.Fatalf("Validate() = %v (%T), want *ValidationError", err, err)
			}
			if got := ve.Fields(); len(got) != 1 || got[0] != tc.field {
				t.Errorf("Fields() = %v, want [%s]", got, tc.field)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("error %q does not name %s", err, tc.field)
			}
		})
	}

	t.Run("mcp_tool without target", func(t *testing.T) {
		h := mcp
		h.MCPTool = hooks.MCPToolRef{Server: "s"}
		ve, ok := asValidation(h.Validate())
		if !ok || len(ve.Fields()) != 1 || ve.Fields()[0] != "MCPTool" {
			t.Errorf("got %v", h.Validate())
		}
	})
}

func TestHookValidateReportsEveryProblem(t *testing.T) {
	err := hooks.Hook{}.Validate()
	ve, ok := asValidation(err)
	if !ok {
		t.Fatalf("Validate() = %v", err)
	}
	want := []string{"Name", "Event", "Kind", "Timeout", "OnError"}
	got := ve.Fields()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Fields() = %v, want %v", got, want)
	}
	for _, f := range want {
		if !strings.Contains(err.Error(), f) {
			t.Errorf("error %q does not name %s", err, f)
		}
	}
}

func h(name string, ev hooks.Event, to time.Duration) hooks.Hook {
	x := validHook()
	x.Name, x.Event, x.Timeout = name, ev, to
	return x
}

func TestResolveDisjointAllSurvive(t *testing.T) {
	got, err := hooks.Resolve(
		[]hooks.Hook{h("m", hooks.EventStop, 1)},
		[]hooks.Hook{h("u", hooks.EventStop, 1)},
		[]hooks.Hook{h("p", hooks.EventStop, 1)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Name != "m" || got[1].Name != "u" || got[2].Name != "p" {
		t.Fatalf("got %+v", got)
	}
	if got[0].Layer != hooks.LayerManaged || got[1].Layer != hooks.LayerUser || got[2].Layer != hooks.LayerProject {
		t.Errorf("layers not stamped: %v %v %v", got[0].Layer, got[1].Layer, got[2].Layer)
	}
}

func TestResolveManagedWinsWholeHook(t *testing.T) {
	m := h("guard", hooks.EventPreToolUse, 5*time.Second)
	m.OnError = hooks.OnErrorClosed
	u := h("guard", hooks.EventPostToolUse, 9*time.Second)
	u.OnError = hooks.OnErrorOpen
	p := h("guard", hooks.EventStop, 30*time.Second)
	p.Matcher = "Bash"
	got, err := hooks.Resolve([]hooks.Hook{m}, []hooks.Hook{u}, []hooks.Hook{p})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d hooks, want 1", len(got))
	}
	want := m
	want.Layer = hooks.LayerManaged
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("not a whole-hook replace (no field merge):\n got %+v\nwant %+v", got[0], want)
	}
}

func TestResolveUserBeatsProject(t *testing.T) {
	u := h("x", hooks.EventStop, 2*time.Second)
	p := h("x", hooks.EventStop, 3*time.Second)
	got, err := hooks.Resolve(nil, []hooks.Hook{u}, []hooks.Hook{p})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Layer != hooks.LayerUser || got[0].Timeout != 2*time.Second {
		t.Fatalf("got %+v", got)
	}
}

func TestResolveEmpty(t *testing.T) {
	got, err := hooks.Resolve(nil, nil, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestResolveErrors(t *testing.T) {
	if _, err := hooks.Resolve(nil, []hooks.Hook{h("a", hooks.EventStop, 1), h("a", hooks.EventStop, 1)}, nil); err == nil {
		t.Error("duplicate name inside one layer accepted")
	}
	if _, err := hooks.Resolve(nil, nil, []hooks.Hook{h("", hooks.EventStop, 1)}); err == nil {
		t.Error("empty name accepted")
	}
}

func TestResolveDoesNotMutateInputs(t *testing.T) {
	m := h("a", hooks.EventStop, 1)
	m.CommandArgs = []string{"x", "y"}
	u := h("a", hooks.EventStop, 2)
	p := h("b", hooks.EventStop, 3)
	mm, uu, pp := []hooks.Hook{m}, []hooks.Hook{u}, []hooks.Hook{p}
	snapM := append([]hooks.Hook(nil), mm...)
	snapU := append([]hooks.Hook(nil), uu...)
	snapP := append([]hooks.Hook(nil), pp...)
	snapArgs := append([]string(nil), m.CommandArgs...)

	got, err := hooks.Resolve(mm, uu, pp)
	if err != nil {
		t.Fatal(err)
	}
	// Mutating the output must not reach the inputs.
	got[0].CommandArgs[0] = "changed"
	got[0].Name = "changed"

	if !reflect.DeepEqual(mm, snapM) || !reflect.DeepEqual(uu, snapU) || !reflect.DeepEqual(pp, snapP) {
		t.Error("Resolve mutated an input slice")
	}
	if !reflect.DeepEqual(mm[0].CommandArgs, snapArgs) {
		t.Error("output aliases input CommandArgs")
	}
	if mm[0].Layer != "" {
		t.Error("Layer written back into the input")
	}
}

func TestResolveDeterministic(t *testing.T) {
	mm := []hooks.Hook{h("a", hooks.EventStop, 1), h("b", hooks.EventStop, 1)}
	uu := []hooks.Hook{h("b", hooks.EventStop, 2), h("c", hooks.EventStop, 2)}
	pp := []hooks.Hook{h("c", hooks.EventStop, 3), h("d", hooks.EventStop, 3)}
	first, err := hooks.Resolve(mm, uu, pp)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		again, err := hooks.Resolve(mm, uu, pp)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("run %d differs:\n%+v\n%+v", i, first, again)
		}
	}
}

func TestResolveOutputValidatesIndependentlyOfLosingLayer(t *testing.T) {
	bad := h("g", hooks.EventStop, 1)
	bad.OnError = "" // the project definition is invalid...
	m := h("g", hooks.EventStop, 1)
	got, err := hooks.Resolve([]hooks.Hook{m}, nil, []hooks.Hook{bad})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range got { // ...but validation runs on the resolved set.
		if err := x.Validate(); err != nil {
			t.Errorf("resolved hook invalid: %v", err)
		}
	}
}

func TestMatchesTool(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"", "Bash", true},
		{"*", "anything", true},
		{"**", "anything", true},
		// exact names and lists
		{"Bash", "Bash", true},
		{"Bash", "Edit", false},
		{"bash", "Bash", false},
		{"Bash", "BashOutput", false}, // exact, not a substring
		{"Edit|Write", "Write", true},
		{"Edit|Write", "Bash", false},
		{"Edit, Write", "Write", true},
		{"Edit,Write", "Edit", true},
		{"mcp__memory__write", "mcp__memory__write", true},
		{"mcp__memory__write", "mcp__memory__write2", false},
		{"Bash", "", false},
		// anything else is an unanchored regex
		{"^Notebook", "NotebookEdit", true},
		{"^Notebook", "MyNotebook", false},
		{"Notebook", "MyNotebookEdit", false}, // name-list token: exact
		{"Notebook.*", "MyNotebookEdit", true},
		{"mcp__memory__.*", "mcp__memory__write", true},
		{"mcp__memory__.*", "mcp__other__write", false},
		{"^(Edit|Write)$", "Write", true},
		{"^(Edit|Write)$", "EditX", false},
		{"[BE].*", "Edit", true},
		{"mcp__memory__*", "mcp__memory__write", true}, // regex: '_*' then unanchored; matches as a substring
		{"mcp__memory__*", "dev_edit", false},
		{"[", "[", false}, // does not compile: matches nothing
		{"(", "(", false},
	}
	for _, tc := range cases {
		if got := hooks.MatchesTool(tc.pattern, tc.name); got != tc.want {
			t.Errorf("MatchesTool(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

func TestTruncateContext(t *testing.T) {
	cases := []struct {
		in    string
		limit int
		want  string
	}{
		{"hello", 0, "hello"},
		{"hello", -3, "hello"},
		{"hello", 5, "hello"},
		{"hello", 9, "hello"},
		{"hello", 3, "hel"},
		{"", 3, ""},
		{"héllo", 2, "h"},  // does not split the two-byte rune
		{"héllo", 3, "hé"}, // exactly on a rune boundary
		{"日本語", 4, "日"},
		{"日本語", 2, ""},
	}
	for _, tc := range cases {
		got := hooks.TruncateContext(tc.in, tc.limit)
		if got != tc.want {
			t.Errorf("TruncateContext(%q, %d) = %q, want %q", tc.in, tc.limit, got, tc.want)
		}
		if tc.limit > 0 && len(got) > tc.limit {
			t.Errorf("result exceeds limit")
		}
	}
}

func TestOnErrorHasNoDefault(t *testing.T) {
	var zero hooks.OnError
	if zero.Valid() {
		t.Fatal("the zero OnError must be invalid")
	}
}
