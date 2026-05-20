// Package main demonstrates how to inform a provider WHAT to cache for a
// given chat call by setting ChatRequest.CacheHints. Each provider decides
// HOW to translate the hints into its own caching primitive (e.g.,
// Anthropic's cache_control: {type: "ephemeral"} markers).
//
// Hints travel with the request, so concurrent callers can't clobber each
// other's caching directives. The older CacheableProvider.SetCacheHints
// pattern stored hints on the provider singleton and is deprecated.
package main

import (
	"context"
	"fmt"

	llmcontracts "github.com/hollis-labs/go-llm-contracts"
	llmtypes "github.com/hollis-labs/go-llm-types"
)

// cachingProvider is a minimal Provider that reads cache hints from each
// request. A real adapter would translate them into provider-specific
// cache markers when serializing the body.
type cachingProvider struct{}

func (cachingProvider) StreamChat(_ context.Context, req llmtypes.ChatRequest) (<-chan llmtypes.StreamEvent, error) {
	for _, h := range req.CacheHints {
		fmt.Printf("apply cache hint: position=%s index=%d\n", h.Position, h.Index)
	}
	ch := make(chan llmtypes.StreamEvent)
	close(ch)
	return ch, nil
}
func (cachingProvider) Complete(context.Context, llmtypes.ChatRequest) (string, error) {
	return "", nil
}
func (cachingProvider) Capabilities() llmtypes.ProviderCapabilities {
	return llmtypes.ProviderCapabilities{SupportsSystemPromptCaching: true}
}

// Compile-time check.
var _ llmcontracts.Provider = cachingProvider{}

func main() {
	p := cachingProvider{}

	req := llmtypes.ChatRequest{
		Model:        "claude-3-5-sonnet",
		SystemPrompt: "You are a concise assistant.",
		Messages: []llmtypes.ChatMessage{
			{Role: "user", Content: "What is the capital of France?"},
		},
		// Apply the standard four-hint strategy: system prompt, tools
		// sentinel, and the two most recent user messages.
		CacheHints: llmcontracts.DefaultCacheStrategy(),
	}

	if _, err := p.StreamChat(context.Background(), req); err != nil {
		fmt.Println("error:", err)
	}
}
