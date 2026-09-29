package reflexes

import "testing"

// FuzzEvaluateTrigger: agent- and operator-authored trigger JSON and regexes
// must never panic; an error or a bool is the only acceptable outcome.
func FuzzEvaluateTrigger(f *testing.F) {
	seeds := []struct{ kind, spec string }{
		{"predicate", `{"kind":"AND","clauses":[{"kind":"tool_calls_window","window":3,"op":"=","value":0}]}`},
		{"predicate", `{"kind":"regex_match_window","window":1,"pattern":"(?i)a+b"}`},
		{"predicate", `{"kind":"text_regex_window","scope":"all","window":2,"pattern":"["}`},
		{"predicate", `{"kind":"entity_mention_window","entity":"task_id","scope":"user","window":1}`},
		{"predicate", `{"kind":"tool_name_window","window":2,"names":["a","b"],"mode":"all","pattern":"x.*"}`},
		{"predicate", `{"kind":"attr","key":"scope_tier","op":"!=","value":"x"}`},
		{"predicate", `{"kind":"output_growth_window","window":3,"factor":1e308}`},
		{"predicate", `{"kind":"prefix_pressure","ratio":-1,"context_window":0}`},
		{"predicate", `{"kind":"OR","clauses":[1]}`},
		{"event", `{"name":"mail_received"}`},
		{"interval", `{"every_n_ticks":-3}`},
		{"nope", `{}`},
		{"predicate", `[[[[[[[[`},
	}
	for _, s := range seeds {
		f.Add(s.kind, s.spec, "task NAN-12 see ./a/b https://x.y", 3)
	}
	f.Fuzz(func(t *testing.T, kind, spec, content string, n int) {
		msgs := []MessageSignal{
			{Content: content, ToolNames: []string{content}, EnvelopeTypes: []string{content}, InputTokens: n, OutputTokens: n * 2, CacheRead: n, ToolCalls: n},
			{Content: content, OutputTokens: n},
			{Content: "x"},
		}
		st := State{
			Messages: msgs, UserMessages: msgs, TickN: n, PrefixTokens: n, MailUnreadCount: n,
			Events: []EventSignal{{EventType: content}}, Attrs: map[string]string{"scope_tier": content, content: content},
		}
		_, _ = EvaluateTrigger(kind, spec, st)
	})
}

func TestEvaluator_AttrPredicate(t *testing.T) {
	st := State{Attrs: map[string]string{"region": "eu", "scope_tier": "open", "execution_pattern": "subagent"}}
	cases := []struct {
		name, spec string
		want, err  bool
	}{
		{"attr_eq", `{"kind":"attr","key":"region","value":"eu"}`, true, false},
		{"attr_ne", `{"kind":"attr","key":"region","op":"!=","value":"us"}`, true, false},
		{"attr_mismatch", `{"kind":"attr","key":"region","value":"us"}`, false, false},
		{"attr_missing_key_is_empty", `{"kind":"attr","key":"absent","value":""}`, true, false},
		{"attr_requires_key", `{"kind":"attr","value":"eu"}`, false, true},
		{"scope_tier_is_not_a_predicate_kind", `{"kind":"scope_tier","value":"open"}`, false, true},
		{"execution_pattern_is_not_a_predicate_kind", `{"kind":"execution_pattern","value":"subagent"}`, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := EvaluateTrigger("predicate", c.spec, st)
			if (err != nil) != c.err || got != c.want {
				t.Errorf("got %v err %v; want %v err=%v", got, err, c.want, c.err)
			}
		})
	}
}
