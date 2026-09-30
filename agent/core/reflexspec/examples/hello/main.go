package main

import (
	"fmt"

	reflexspec "github.com/hollis-labs/reflex-definition-spec"
)

func main() {
	d := reflexspec.Definition{
		Name:        "idle-drift",
		TriggerKind: "predicate",
		// "tool_call_window" is one letter short of "tool_calls_window".
		TriggerSpec: `{"kind":"tool_call_window","window":3,"op":"=","value":0}`,
		ActionKind:  "inject_reminder",
		ActionSpec:  `{"body":"State the next concrete action.","urgency":"info"}`,
	}
	for _, err := range reflexspec.Validate(d) {
		fmt.Println(err)
	}
}
