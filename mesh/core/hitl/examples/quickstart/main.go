package main

import (
	"context"
	"fmt"
	"log"

	"github.com/hollis-labs/substrate/mesh/hitl"
	"github.com/hollis-labs/substrate/mesh/hitl/memstore"
)

func main() {
	ctx := context.Background()
	svc := hitl.NewService(memstore.New(), hitl.Options{})

	// A requester asks; the handle is all it keeps.
	handle, err := svc.Enqueue(ctx, hitl.EnqueueRequest{
		ContractVersion: hitl.ContractVersion,
		Kind:            "approval",
		IdempotencyKey:  "deploy:r17",
		Source:          hitl.SourceAssertion{ApplicationID: "ci", AgentID: "release-bot"},
	})
	if err != nil {
		log.Fatal(err)
	}

	// A human answers. The proof slot carries, but does not verify, a credential.
	_, err = svc.Respond(ctx, hitl.RespondCommand{
		ItemID:   handle.ItemID,
		Response: hitl.Response{Kind: "approval", Decision: "approved"},
		Participant: hitl.Participant{
			Responder: &hitl.Responder{Kind: "human", Ref: "operator@example.test"},
			Assurance: "authenticated",
			Proof: &hitl.Proof{Scheme: "webauthn", KeyRef: "cred:3f9a", Binds: []hitl.ProofBind{
				{Name: "approval_id", Value: handle.ItemID},
			}},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	// Anyone with the handle sees the one immutable outcome; a late withdraw loses.
	caller := hitl.CallerAssertion{ApplicationID: "ci"}
	got, err := svc.Get(ctx, hitl.GetCommand{ContractVersion: hitl.ContractVersion, ItemID: handle.ItemID, Caller: caller})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(got.Item.State, got.Item.TerminalOutcome.OutcomeState())

	_, err = svc.Withdraw(ctx, hitl.WithdrawCommand{ContractVersion: hitl.ContractVersion, ItemID: handle.ItemID, Caller: caller})
	fmt.Println(err != nil)
}
