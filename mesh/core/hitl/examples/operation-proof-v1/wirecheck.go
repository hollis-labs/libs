package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/hollis-labs/libs/mesh/core/hitl"
	"github.com/hollis-labs/libs/mesh/core/hitl/memstore"
	"os"
	"reflect"
	"time"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func main() {
	raw, err := os.ReadFile(os.Args[1])
	must(err)
	var doc struct {
		Cases []struct {
			Name        string
			Operation   json.RawMessage
			Response    hitl.Response
			Participant hitl.Participant
		}
	}
	must(json.Unmarshal(raw, &doc))
	ctx := context.Background()
	for _, c := range doc.Cases {
		svc := hitl.NewService(memstore.New(), hitl.Options{Clock: func() time.Time { return time.Unix(1900000010, 0) }, NewID: func() string { return "item-example-1" }})
		h, err := svc.Enqueue(ctx, hitl.EnqueueRequest{ContractVersion: hitl.ContractVersion, Kind: "approval", IdempotencyKey: c.Name, Source: hitl.SourceAssertion{ApplicationID: "example", AgentID: "agent"}, Extra: map[string]json.RawMessage{"operation": c.Operation}})
		must(err)
		_, err = svc.Respond(ctx, hitl.RespondCommand{ItemID: h.ItemID, Response: c.Response, Participant: c.Participant})
		must(err)
		result, err := svc.Get(ctx, hitl.GetCommand{ContractVersion: hitl.ContractVersion, ItemID: h.ItemID, Caller: hitl.CallerAssertion{ApplicationID: "example"}})
		must(err)
		wire, err := json.Marshal(result)
		must(err)
		var round hitl.RetrievalResult
		must(json.Unmarshal(wire, &round))
		resolved := round.Item.TerminalOutcome.(hitl.Resolved)
		var before, after any
		must(json.Unmarshal(c.Response.Extra["assurance_evidence"], &before))
		must(json.Unmarshal(resolved.Resolution.Response.Extra["assurance_evidence"], &after))
		if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(c.Participant, resolved.Resolution.Participant) {
			panic(c.Name + ": evidence lost")
		}
	}
	fmt.Printf("Round-tripped %d evidence envelopes through Enqueue/Respond/Get and JSON; core carries all, including invalid proofs.\n", len(doc.Cases))
}
