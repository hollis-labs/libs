// Package main demonstrates the minimum implementation surface required to
// satisfy the llmcontracts.Provider interface. The example uses a stub adapter
// that emits a single delta event and a fixed Complete response — replace
// the body of each method with your real HTTP/SDK call.
package main

import (
	"context"
	"fmt"

	llmcontracts "github.com/hollis-labs/go-llm-contracts"
	llmtypes "github.com/hollis-labs/go-llm-types"
)

// stubProvider is a minimal Provider implementation. A real adapter would
// translate llmtypes.ChatRequest into a provider-native HTTP/SDK call.
type stubProvider struct {
	name string
}

func (p stubProvider) StreamChat(ctx context.Context, req llmtypes.ChatRequest) (<-chan llmtypes.StreamEvent, error) {
	out := make(chan llmtypes.StreamEvent, 2)
	out <- llmtypes.StreamEvent{Type: llmtypes.EventDelta, Content: "hello from " + p.name}
	out <- llmtypes.StreamEvent{Type: llmtypes.EventDone}
	close(out)
	return out, nil
}

func (p stubProvider) Complete(ctx context.Context, req llmtypes.ChatRequest) (string, error) {
	return "stub completion from " + p.name, nil
}

func (p stubProvider) Capabilities() llmtypes.ProviderCapabilities {
	return llmtypes.ProviderCapabilities{
		SupportsToolCalling: false,
		MaxTokens:           4096,
	}
}

// Compile-time check that stubProvider satisfies the contract.
var _ llmcontracts.Provider = stubProvider{}

func main() {
	var p llmcontracts.Provider = stubProvider{name: "demo"}

	text, err := p.Complete(context.Background(), llmtypes.ChatRequest{})
	if err != nil {
		panic(err)
	}
	fmt.Println("Complete:", text)

	stream, err := p.StreamChat(context.Background(), llmtypes.ChatRequest{})
	if err != nil {
		panic(err)
	}
	for ev := range stream {
		fmt.Printf("StreamEvent: type=%s content=%q\n", ev.Type, ev.Content)
	}
}
