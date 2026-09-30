package reflexspec

import (
	"encoding/json"
	"testing"
)

// FuzzValidate: no input, however malformed, may panic or hang the validator,
// and Validate must be deterministic.
func FuzzValidate(f *testing.F) {
	for _, s := range validLeaf {
		f.Add("predicate", s, "inject_reminder", `{"body":"x"}`)
	}
	f.Add("event", `{"name":"mail_received"}`, "halt_session", `{}`)
	f.Add("interval", `{"every_n_ticks":20}`, "dispatch_to_agent", `{"agent_slug":"a","confidence":0.5}`)
	f.Add("predicate", `{"kind":"AND","clauses":[{"kind":"OR","clauses":[{}]}]}`, "", ``)
	f.Add("predicate", `{"kind":"attr","key":"k","window":1e999}`, "x", `[`)
	f.Fuzz(func(t *testing.T, tk, ts, ak, as string) {
		d := Definition{Name: "n", TriggerKind: tk, TriggerSpec: ts, ActionKind: ak, ActionSpec: as}
		a := Validate(d)
		b := Validate(d)
		if len(a) != len(b) {
			t.Fatalf("non-deterministic: %d vs %d", len(a), len(b))
		}
		_ = ValidateTriggerSpec(tk, ts)
		_ = ValidateActionSpec(ak, as)
	})
}

// FuzzValidateDefinitionJSON feeds whole records as JSON.
func FuzzValidateDefinitionJSON(f *testing.F) {
	f.Add([]byte(`{"name":"n","trigger_kind":"event","trigger_spec":"{\"name\":\"e\"}","action_kind":"halt_session","action_spec":"{}"}`))
	f.Add([]byte(`{"recurrence_override_seconds":-5}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var d Definition
		if json.Unmarshal(data, &d) != nil {
			return
		}
		_ = Validate(d)
	})
}
