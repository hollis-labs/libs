package reflexspec_test

import (
	"errors"
	"fmt"

	reflexspec "github.com/hollis-labs/reflex-definition-spec"
)

func ExampleValidate() {
	d := reflexspec.Definition{
		Name:        "idle-drift",
		TriggerKind: "predicate",
		TriggerSpec: `{"kind":"tool_call_window","window":3}`, // one letter short
		ActionKind:  "inject_reminder",
		ActionSpec:  `{"body":"State the next concrete action."}`,
	}
	for _, err := range reflexspec.Validate(d) {
		fmt.Println(err)
	}
	// Output:
	// trigger_spec.kind: unknown predicate kind "tool_call_window"; the engine would treat it as never firing [unknown_predicate_kind]
}

func ExampleValidateTriggerSpec() {
	err := reflexspec.ValidateTriggerSpec("predicate",
		`{"kind":"AND","clauses":[{"kind":"regex_match_window","window":2},{"kind":"attr","key":"scope"}]}`)
	var e *reflexspec.Error
	if errors.As(err, &e) {
		fmt.Println(e.Field, e.Code)
	}
	// Output: trigger_spec.clauses[0].pattern required
}

func ExampleValidateActionSpec() {
	fmt.Println(reflexspec.ValidateActionSpec("dispatch_to_agent", `{"agent_slug":"planner","confidence":0.75}`))
	fmt.Println(reflexspec.ValidateActionSpec("send_message", `{"any":"object"}`))
	// Output:
	// <nil>
	// <nil>
}

func ExampleWithKnownActionKinds() {
	known := func(k string) bool { return k == "halt_session" }
	fmt.Println(reflexspec.ValidateActionSpec("halt_sesion", `{}`, reflexspec.WithKnownActionKinds(known)))
	// Output: action_kind: action_kind "halt_sesion" is not in the configured catalog [not_in_catalog]
}

func ExamplePredicateKinds() {
	kinds := reflexspec.PredicateKinds()
	fmt.Println(kinds[0], kinds[1]) // the combinators sort first
	// Output: AND OR
}
