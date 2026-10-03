package contracts_test

import (
	"fmt"

	agentcontracts "github.com/hollis-labs/substrate/llm-core/contracts"
)

func ExampleAssignment_Validate() {
	a := agentcontracts.Assignment{
		Agent: agentcontracts.AgentRef{Name: "incident-triage"},
		Run: agentcontracts.RunPolicy{
			Lifetime: agentcontracts.LifetimeOneShot,
			Resume:   agentcontracts.ResumeNever,
		},
		Task: agentcontracts.Task{Input: "triage alert 4412"},
	}
	fmt.Println(a.Validate())

	a.Task.Input = ""
	fmt.Println(a.Validate())
	// Output:
	// <nil>
	// task.input is required
}

func ExampleEffectiveTrust() {
	fmt.Println(agentcontracts.EffectiveTrust(agentcontracts.TrustTrusted, agentcontracts.TrustNormal))
	// Output: normal
}
