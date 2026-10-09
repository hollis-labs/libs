package agentmux_test

import (
	"context"
	"fmt"
	"log"

	agentmux "github.com/hollis-labs/substrate/mesh/agentmuxclient"
)

func ExampleClient_CreateEnvelope_naniteHostChat() {
	ctx := context.Background()
	client := agentmux.MustNew("tcp:127.0.0.1:7180")

	env, err := client.CreateEnvelope(ctx, agentmux.EnvelopeCreateRequest{
		Sender:      "relay",
		Recipient:   "nanite",
		WorkflowID:  "mux-http-smoke-test",
		MessageType: "request",
		Priority:    0,
		Payload:     `{"prompt":"Host this relay-chat smoke test over the HTTP API."}`,
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(env.ID)
}
