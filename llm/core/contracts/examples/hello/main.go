package main

import (
	"fmt"

	agentcontracts "github.com/hollis-labs/agent-contracts-leaf"
)

func main() {
	a := agentcontracts.Assignment{
		Agent: agentcontracts.AgentRef{Name: "incident-triage"},
		Run:   agentcontracts.RunPolicy{Lifetime: agentcontracts.LifetimeOneShot, Resume: agentcontracts.ResumeNever},
		Task:  agentcontracts.Task{Input: "triage alert 4412"},
	}
	if err := a.Validate(); err != nil {
		fmt.Println("invalid:", err)
		return
	}

	// The host that launches it records what it actually granted.
	rec := agentcontracts.LaunchRecord{
		Digests:        agentcontracts.Digests{Assignment: "sha256:..."},
		EffectiveTrust: agentcontracts.EffectiveTrust(agentcontracts.TrustTrusted, agentcontracts.TrustNormal),
	}
	fmt.Println(rec.Digests.Assignment, rec.EffectiveTrust)
}
