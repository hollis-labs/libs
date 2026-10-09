package reflexes

import (
	"strings"
	"testing"
)

// TestEvaluator_NumericWindowKinds exercises the basic per-signal
// predicate kinds in isolation.
func TestEvaluator_NumericWindowKinds(t *testing.T) {
	signals := []MessageSignal{
		{ToolCalls: 0, CacheRead: 0, InputTokens: 3},
		{ToolCalls: 0, CacheRead: 0, InputTokens: 5},
		{ToolCalls: 0, CacheRead: 0, InputTokens: 7},
	}
	st := State{Messages: signals}

	cases := []struct {
		name string
		spec string
		want bool
	}{
		{"toolcalls_eq_0", `{"kind":"tool_calls_window","window":3,"op":"=","value":0}`, true},
		{"toolcalls_lt_1", `{"kind":"tool_calls_window","window":3,"op":"<","value":1}`, true},
		{"input_lt_10", `{"kind":"input_tokens_window","window":3,"op":"<","value":10}`, true},
		{"input_lt_4", `{"kind":"input_tokens_window","window":3,"op":"<","value":4}`, false},
		{"cache_eq_0", `{"kind":"cache_read_window","window":3,"op":"=","value":0}`, true},
		{"window_too_large_returns_false", `{"kind":"tool_calls_window","window":99,"op":"=","value":0}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := EvaluateTrigger("predicate", c.spec, st)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != c.want {
				t.Errorf("got %v want %v", got, c.want)
			}
		})
	}
}

// TestEvaluator_IdenticalOutputWindow_ByteIdentity confirms the helper.
func TestEvaluator_IdenticalOutputWindow_ByteIdentity(t *testing.T) {
	st := State{Messages: []MessageSignal{
		{Content: "hello"}, {Content: "hello"}, {Content: "hello"},
	}}
	got, _ := EvaluateTrigger("predicate", `{"kind":"identical_output_window","window":3}`, st)
	if !got {
		t.Errorf("identical bodies should fire identical_output_window; got false")
	}
	st.Messages[1].Content = "world"
	got, _ = EvaluateTrigger("predicate", `{"kind":"identical_output_window","window":3}`, st)
	if got {
		t.Errorf("differing bodies should NOT fire; got true")
	}
}

// TestEvaluator_OutputGrowthWindow validates the 1.5× growth check.
func TestEvaluator_OutputGrowthWindow(t *testing.T) {
	growing := State{Messages: []MessageSignal{
		{OutputTokens: 40}, {OutputTokens: 20}, {OutputTokens: 10},
	}}
	got, _ := EvaluateTrigger("predicate", `{"kind":"output_growth_window","window":3,"factor":1.5}`, growing)
	if !got {
		t.Errorf("growing 10→20→40 should fire 1.5× growth; got false")
	}
	flat := State{Messages: []MessageSignal{
		{OutputTokens: 10}, {OutputTokens: 10}, {OutputTokens: 10},
	}}
	got, _ = EvaluateTrigger("predicate", `{"kind":"output_growth_window","window":3,"factor":1.5}`, flat)
	if got {
		t.Errorf("flat 10/10/10 should NOT fire growth; got true")
	}
}

// TestEvaluator_RegexMatchWindow_AtLeastOne validates "any match in window."
func TestEvaluator_RegexMatchWindow_AtLeastOne(t *testing.T) {
	st := State{Messages: []MessageSignal{
		{Content: "ok"},
		{Content: "Beyond Emergency"},
		{Content: "fine"},
	}}
	spec := `{"kind":"regex_match_window","window":3,"pattern":"(?i)(Beyond|Absolute|Ultimate|Maximum)\\s+(Emergency|Crisis|Unprecedented)"}`
	got, err := EvaluateTrigger("predicate", spec, st)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !got {
		t.Errorf("expected match (Beyond Emergency); got false")
	}
}

// TestEvaluator_EventTrigger_MailReceived exercises the event path.
func TestEvaluator_EventTrigger_MailReceived(t *testing.T) {
	st := State{MailUnreadCount: 0}
	got, _ := EvaluateTrigger("event", `{"name":"mail_received"}`, st)
	if got {
		t.Errorf("no unread → should not fire")
	}
	st.MailUnreadCount = 2
	got, _ = EvaluateTrigger("event", `{"name":"mail_received"}`, st)
	if !got {
		t.Errorf("unread=2 → should fire")
	}
}

// TestEvaluator_IntervalTrigger_FiresOnMultiple exercises interval kind.
func TestEvaluator_IntervalTrigger_FiresOnMultiple(t *testing.T) {
	st := State{TickN: 20}
	got, _ := EvaluateTrigger("interval", `{"every_n_ticks":10}`, st)
	if !got {
		t.Errorf("tick=20 every_n=10 should fire; got false")
	}
	st.TickN = 15
	got, _ = EvaluateTrigger("interval", `{"every_n_ticks":10}`, st)
	if got {
		t.Errorf("tick=15 should NOT fire every-10; got true")
	}
}

// TestEvaluator_PrefixPressure exercises the prefix_pressure node.
func TestEvaluator_PrefixPressure(t *testing.T) {
	// 170k of 200k window = 0.85 ratio — at the threshold.
	st := State{PrefixTokens: 170000}
	spec := `{"kind":"prefix_pressure","ratio":0.85,"context_window":200000}`
	got, _ := EvaluateTrigger("predicate", spec, st)
	if !got {
		t.Errorf("170k/200k should fire prefix_pressure(0.85); got false")
	}
	st.PrefixTokens = 100000
	got, _ = EvaluateTrigger("predicate", spec, st)
	if got {
		t.Errorf("100k/200k should NOT fire prefix_pressure(0.85); got true")
	}
}

func TestEvaluator_UserRegexWindow(t *testing.T) {
	st := State{UserMessages: []MessageSignal{
		{Role: "user", Content: "Let's document that and create a task for it."},
	}}
	spec := `{"kind":"user_regex_window","window":1,"pattern":"(?i)document that.*create a task"}`
	got, err := EvaluateTrigger("predicate", spec, st)
	if err != nil {
		t.Fatalf("EvaluateTrigger error: %v", err)
	}
	if !got {
		t.Fatalf("user_regex_window should match recent user intent")
	}
}

func TestEvaluator_EntityMentionWindow(t *testing.T) {
	st := State{UserMessages: []MessageSignal{
		{Role: "user", Content: "Can you check NAN-128 and docs/promptrouter-authoring.md?"},
	}}
	got, err := EvaluateTrigger("predicate", `{"kind":"entity_mention_window","entity":"task_id","scope":"user","window":1}`, st)
	if err != nil {
		t.Fatalf("task id EvaluateTrigger error: %v", err)
	}
	if !got {
		t.Fatalf("entity_mention_window should match task ids")
	}
	got, err = EvaluateTrigger("predicate", `{"kind":"entity_mention_window","entity":"path","scope":"user","window":1}`, st)
	if err != nil {
		t.Fatalf("path EvaluateTrigger error: %v", err)
	}
	if !got {
		t.Fatalf("entity_mention_window should match file paths")
	}
}

func TestEvaluator_ToolAndEnvelopeNameWindows(t *testing.T) {
	st := State{Messages: []MessageSignal{
		{Role: "assistant", ToolNames: []string{"memory_write", "torque_task_create"}, EnvelopeTypes: []string{"options"}},
		{Role: "assistant", ToolNames: []string{"relay_send"}, EnvelopeTypes: []string{"status"}},
	}}
	got, err := EvaluateTrigger("predicate", `{"kind":"tool_name_window","window":2,"names":["torque_task_create"],"mode":"any"}`, st)
	if err != nil {
		t.Fatalf("tool_name_window error: %v", err)
	}
	if !got {
		t.Fatalf("tool_name_window should match named tool")
	}
	got, err = EvaluateTrigger("predicate", `{"kind":"envelope_type_window","window":2,"names":["approval"],"mode":"none"}`, st)
	if err != nil {
		t.Fatalf("envelope_type_window error: %v", err)
	}
	if !got {
		t.Fatalf("envelope_type_window mode=none should fire when absent")
	}
}

func TestEvaluator_MailUnreadCountPredicate(t *testing.T) {
	st := State{MailUnreadCount: 3}
	got, err := EvaluateTrigger("predicate", `{"kind":"mail_unread_count","op":">=","value":2}`, st)
	if err != nil {
		t.Fatalf("EvaluateTrigger error: %v", err)
	}
	if !got {
		t.Fatalf("mail_unread_count should compare unread mailbox count")
	}
}

// TestEvaluator_UnknownKindReturnsError keeps the engine honest.
func TestEvaluator_UnknownKindReturnsError(t *testing.T) {
	_, err := EvaluateTrigger("predicate", `{"kind":"nonexistent"}`, State{})
	if err == nil || !strings.Contains(err.Error(), "unknown predicate kind") {
		t.Errorf("expected unknown-kind error; got %v", err)
	}
}
