package agentmux_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	agentmux "github.com/hollis-labs/substrate/mesh/agentmuxclient"
	messaging "github.com/hollis-labs/substrate/mesh/messaging"
)

// ExampleClient_AsDispatcher shows the canonical cross-system request/reply
// pattern using the go-messaging Dispatcher contract.
func ExampleClient_AsDispatcher() {
	client := agentmux.MustNew("")
	disp := client.AsDispatcher()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := disp.Request(ctx, messaging.Envelope{
		From:    messaging.Address{Kind: messaging.KindAgent, Authority: "nanite", ID: "alice"},
		To:      messaging.Address{Kind: messaging.KindAgent, Authority: "agent-mux", ID: "relay"},
		Payload: json.RawMessage(`{"prompt":"hello"}`),
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(resp.ID)
}

// ExampleClient_AsStore shows direct Store usage for sending and retrieving
// a message via the /messages/* HTTP routes.
func ExampleClient_AsStore() {
	client := agentmux.MustNew("")
	store := client.AsStore()

	ctx := context.Background()

	sent, err := store.Send(ctx, messaging.Envelope{
		Kind:    messaging.MsgKindNotice,
		From:    messaging.Address{Kind: messaging.KindAgent, Authority: "nanite", ID: "alice"},
		To:      messaging.Address{Kind: messaging.KindAgent, Authority: "clockwork", ID: "scheduler"},
		Payload: json.RawMessage(`{"event":"task_complete"}`),
	})
	if err != nil {
		log.Fatal(err)
	}

	got, err := store.Get(ctx, sent.ID)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(got.ID == sent.ID)
}
