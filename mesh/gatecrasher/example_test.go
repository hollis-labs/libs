package gatecrasher

import (
	"fmt"

	"github.com/hollis-labs/libs/mesh/core/gate"
)

// The example uses the real HITL Service and deterministic fake host ports from
// the behavioral suite. Production supplies its own authenticated owner,
// policy/identity bindings, durable journal and exact live native Origin.
func ExampleCoordinator() {
	f := newFixture()
	ticket, err := f.c.Request(human(), f.request)
	if err != nil {
		panic(err)
	}
	decision, err := f.c.Respond(human(), ticket, ResponseAttempt{OptionID: "native-allow-once", ExpectedRevision: ticket.Handle.Revision})
	if err != nil {
		panic(err)
	}
	delivery, err := f.c.Deliver(human(), ticket)
	if err != nil {
		panic(err)
	}
	executor := &fakeExecutor{}
	if err = executor.execute(human(), f.request, *decision.Decision, admission(f), f.auth); err != nil {
		panic(err)
	}
	err = f.c.RecordExecution(human(), gate.ExecutionReceipt{DecisionRef: delivery.DecisionRef, OperationID: f.request.OperationID,
		Binding: f.request.Binding, State: gate.ExecutionSucceeded, EffectRef: "fake-effect-1"})
	if err != nil {
		panic(err)
	}
	fmt.Println(decision.State, delivery.Status, executor.effects)
	// Output: decided acknowledged 1
}
