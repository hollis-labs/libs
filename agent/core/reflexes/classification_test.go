package reflexes

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

// These expectations follow Nanite's evaluator.go evalStringEquals, including
// its permissive missing-value coercion and false/nil unknown-op result.
func TestEvaluator_LiveClassification(t *testing.T) {
	for _, kind := range []string{"scope_tier", "execution_pattern"} {
		t.Run(kind, func(t *testing.T) {
			for _, tc := range []struct {
				name, signal, args string
				want               bool
			}{
				{"default-equality", "open", `"value":"open"`, true},
				{"explicit-equality", "open", `"op":"=","value":"open"`, true},
				{"double-equality", "open", `"op":"==","value":"open"`, true},
				{"empty-op-default", "open", `"op":"","value":"open"`, true},
				{"non-string-op-default", "open", `"op":42,"value":"open"`, true},
				{"unequal", "open", `"value":"large"`, false},
				{"inequality-true", "open", `"op":"!=","value":"large"`, true},
				{"inequality-false", "open", `"op":"!=","value":"open"`, false},
				{"case-sensitive", "Open", `"value":"open"`, false},
				{"unknown-op-no-error", "open", `"op":"contains","value":"open"`, false},
				{"unset-non-empty-value", "", `"value":"open"`, false},
				{"unset-inequality", "", `"op":"!=","value":"open"`, true},
				{"unset-empty-value", "", `"value":""`, true},
				{"unset-missing-value", "", `"window":99`, true},
				{"unset-non-string-value", "", `"value":42`, true},
				{"unset-null-value", "", `"value":null`, true},
				{"set-missing-value", "open", `"window":99`, false},
				{"set-non-string-value", "open", `"value":42`, false},
				{"no-history-guard", "open", `"window":99,"value":"open"`, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					st := State{Attrs: map[string]string{kind: "ignored"}}
					if kind == "scope_tier" {
						st.ScopeTier = tc.signal
						st.ExecutionPattern = "other"
					} else {
						st.ExecutionPattern = tc.signal
						st.ScopeTier = "other"
					}
					spec := `{"kind":"` + kind + `",` + tc.args + `}`
					got, err := EvaluateTrigger("predicate", spec, st)
					if err != nil || got != tc.want {
						t.Fatalf("signal=%q spec=%s: got %v/%v, want %v/nil", tc.signal, spec, got, err, tc.want)
					}
				})
			}
		})
	}
}

func TestLiveClassification_JSONAndTrace(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state State
	}{
		{"both", State{ScopeTier: "Open", ExecutionPattern: "Subagent"}},
		{"scope-only", State{ScopeTier: "open"}},
		{"pattern-only", State{ExecutionPattern: "background"}},
		{"unset", State{}},
		{"with-attrs", State{ScopeTier: "open", ExecutionPattern: "subagent", Attrs: map[string]string{"region": "eu"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.state.Events = []EventSignal{{EventType: "probe"}}
			encoded, err := json.Marshal(tc.state)
			if err != nil {
				t.Fatal(err)
			}
			var decoded State
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decoded, tc.state) {
				t.Fatalf("round-trip got %+v, want %+v", decoded, tc.state)
			}
			r := alwaysFireReflex("d", "dispatch", ActionDispatchToAgent, 10, "2026-01-01")
			fs := &fakeStore{kinds: catalog(), rows: []Reflex{r}}
			_, err = newEngine(t, fs).Run(context.Background(), RunInput{State: &decoded})
			if err != nil {
				t.Fatal(err)
			}
			if len(fs.events) != 1 {
				t.Fatalf("events=%v", fs.events)
			}
			for _, raw := range []string{string(encoded), fs.events[0].Metadata} {
				var fields map[string]json.RawMessage
				if err := json.Unmarshal([]byte(raw), &fields); err != nil {
					t.Fatal(err)
				}
				for key, want := range map[string]string{"scope_tier": tc.state.ScopeTier, "execution_pattern": tc.state.ExecutionPattern} {
					data, present := fields[key]
					if present != (want != "") {
						t.Fatalf("%s presence in %s: want populated-only", key, raw)
					}
					if present {
						var got string
						if err := json.Unmarshal(data, &got); err != nil || got != want {
							t.Fatalf("%s=%s, want %q", key, data, want)
						}
					}
				}
				var attrs map[string]string
				if data, present := fields["attrs"]; present {
					if err := json.Unmarshal(data, &attrs); err != nil {
						t.Fatal(err)
					}
				}
				if !reflect.DeepEqual(attrs, tc.state.Attrs) {
					t.Fatalf("attrs=%v, want %v; classification must not be inserted", attrs, tc.state.Attrs)
				}
			}
			var trace traceRecord
			if err := json.Unmarshal([]byte(fs.events[0].Metadata), &trace); err != nil {
				t.Fatal(err)
			}
			if trace.ScopeTier != tc.state.ScopeTier || trace.ExecutionPattern != tc.state.ExecutionPattern {
				t.Fatalf("trace round-trip lost classification: %+v", trace)
			}
		})
	}
}

func TestRunEquivalence_AttrsOnlyHost(t *testing.T) {
	// Existing hosts can keep generic attributes and predicates, including keys
	// named scope_tier/execution_pattern. No automatic promotion or rewriting.
	r := dispatchRow("legacy", 10, `{"agent_slug":"planner"}`)
	r.TriggerSpec = `{"kind":"AND","clauses":[{"kind":"attr","key":"scope_tier","value":"open"},{"kind":"attr","key":"execution_pattern","value":"subagent"},{"kind":"attr","key":"region","value":"eu"}]}`
	st := State{Attrs: map[string]string{"scope_tier": "open", "execution_pattern": "subagent", "region": "eu"}}
	fs := &fakeStore{kinds: catalog(), rows: []Reflex{r}}
	result, err := newEngine(t, fs).Run(context.Background(), RunInput{State: &st})
	if err != nil || len(result.Applied.Actions) != 1 || len(fs.bumps) != 1 {
		t.Fatalf("result=%+v err=%v bumps=%v", result, err, fs.bumps)
	}
	assertTraces(t, fs, []string{`{"reflex_id":"legacy","reflex_name":"dispatch-legacy","action_kind":"dispatch_to_agent","category":"execute_action","combining_algorithm":"first_applicable","priority":10,"attrs":{"execution_pattern":"subagent","region":"eu","scope_tier":"open"},"spec":{"agent_slug":"planner"}}`})
}
