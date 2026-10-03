package hitl_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hollis-labs/go-hitl"
	"github.com/hollis-labs/go-hitl/memstore"
)

func Example() {
	ctx := context.Background()
	svc := hitl.NewService(memstore.New(), hitl.Options{})
	h, _ := svc.Enqueue(ctx, hitl.EnqueueRequest{
		ContractVersion: hitl.ContractVersion, Kind: "approval", IdempotencyKey: "k",
		Source: hitl.SourceAssertion{ApplicationID: "app", AgentID: "agent"},
	})
	caller := hitl.CallerAssertion{ApplicationID: "app"}

	// First terminal wins: the withdrawal below loses to the resolution.
	_, _ = svc.Respond(ctx, hitl.RespondCommand{
		ItemID:      h.ItemID,
		Response:    hitl.Response{Kind: "approval", Decision: "denied"},
		Participant: hitl.Participant{PrincipalRef: "operator", Assurance: "asserted"},
	})
	_, err := svc.Withdraw(ctx, hitl.WithdrawCommand{ContractVersion: hitl.ContractVersion, ItemID: h.ItemID, Caller: caller})
	var conflict *hitl.TerminalConflictError
	fmt.Println(errors.As(err, &conflict), conflict.Outcome.OutcomeState())
	// Output: true resolved
}

func ExampleCanTransition() {
	fmt.Println(hitl.CanTransition(hitl.StatePresented, hitl.StateResolved))
	fmt.Println(hitl.CanTransition(hitl.StateResolved, hitl.StateCanceled)) // terminal states are final
	// Output:
	// true
	// false
}

func ExampleUnmarshalOutcome() {
	o, err := hitl.UnmarshalOutcome([]byte(`{
		"contract_version": "1.0", "state": "canceled", "item_id": "item_1",
		"interaction_revision": 3, "cause": "caller_withdrawn",
		"terminated_at": "2026-09-04T03:31:00Z"}`))
	if err != nil {
		panic(err)
	}
	c := o.(hitl.Canceled)
	fmt.Println(c.OutcomeState(), c.Cause)
	// Output: canceled caller_withdrawn
}

// A request whose expires_at has passed is refused atomically, even though no
// sweeper has run: the store owner enforces the deadline.
func ExampleService_Respond_expiry() {
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	svc := hitl.NewService(memstore.New(), hitl.Options{Clock: func() time.Time { return now }})
	deadline := now.Add(time.Minute)
	h, _ := svc.Enqueue(ctx, hitl.EnqueueRequest{
		ContractVersion: hitl.ContractVersion, Kind: "approval", IdempotencyKey: "k", ExpiresAt: &deadline,
		Source: hitl.SourceAssertion{ApplicationID: "app", AgentID: "agent"},
	})
	now = deadline // time passes; nothing sweeps
	_, err := svc.Respond(ctx, hitl.RespondCommand{
		ItemID:      h.ItemID,
		Response:    hitl.Response{Kind: "approval", Decision: "approved"},
		Participant: hitl.Participant{PrincipalRef: "operator", Assurance: "asserted"},
	})
	var conflict *hitl.TerminalConflictError
	fmt.Println(errors.As(err, &conflict), conflict.Outcome.OutcomeState())
	// Output: true expired
}
