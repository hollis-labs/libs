package tether_test

import (
	"context"
	"fmt"
	"log"

	tether "github.com/hollis-labs/go-tether-client"
)

func ExampleClient_CreateEnvelope_naniteHostChat() {
	ctx := context.Background()
	client := tether.MustNew("tcp:127.0.0.1:7180")

	env, err := client.CreateEnvelope(ctx, tether.EnvelopeCreateRequest{
		Sender:      "relay",
		Recipient:   "nanite",
		WorkflowID:  "tether-http-smoke-test",
		MessageType: "request",
		Priority:    0,
		Payload:     `{"prompt":"Host this relay-chat smoke test over the HTTP API."}`,
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(env.ID)
}
