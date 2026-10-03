package agentcontracts_test

import (
	"fmt"

	agentcontracts "github.com/hollis-labs/agent-contracts-leaf"
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

func ExampleInstanceStatus_ToA2A() {
	stopped := agentcontracts.StoppedDetail{Reason: agentcontracts.ReasonCompleted}
	fmt.Println(agentcontracts.StatusStopped.ToA2A("", stopped))
	fmt.Println(agentcontracts.StatusWaiting.ToA2A(agentcontracts.WaitingAuth, agentcontracts.StoppedDetail{}))
	// Output:
	// completed
	// auth-required
}
