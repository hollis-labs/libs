// Package main demonstrates the llmcontracts.CacheableProvider interface, used
// by callers to inform a provider WHAT to cache before each request. Each
// provider decides HOW to translate the hints into its own caching primitive
// (e.g., Anthropic's cache_control: {type: "ephemeral"} markers).
package main

import (
	"context"
	"fmt"

	llmcontracts "github.com/hollis-labs/go-llm-contracts"
	llmtypes "github.com/hollis-labs/go-llm-types"
)

// cacheableProvider records the latest cache hints. A real adapter would
// translate them into provider-specific cache markers when serializing the
// request body.
type cacheableProvider struct {
	hints []llmcontracts.CacheHint
}

func (p *cacheableProvider) StreamChat(context.Context, llmtypes.ChatRequest) (<-chan llmtypes.StreamEvent, error) {
	ch := make(chan llmtypes.StreamEvent)
	close(ch)
	return ch, nil
}
func (p *cacheableProvider) Complete(context.Context, llmtypes.ChatRequest) (string, error) {
	return "", nil
}
func (p *cacheableProvider) Capabilities() llmtypes.ProviderCapabilities {
	return llmtypes.ProviderCapabilities{SupportsSystemPromptCaching: true}
}

// SetCacheHints stores the hint set the caller wants applied to the next
// request. Implementations should be cheap and idempotent.
func (p *cacheableProvider) SetCacheHints(hints []llmcontracts.CacheHint) {
	p.hints = hints
}

// Compile-time checks.
var (
	_ llmcontracts.Provider          = (*cacheableProvider)(nil)
	_ llmcontracts.CacheableProvider = (*cacheableProvider)(nil)
)

func main() {
	p := &cacheableProvider{}

	// Apply the standard four-hint strategy: system prompt, tools sentinel,
	// and the two most recent user messages.
	p.SetCacheHints(llmcontracts.DefaultCacheStrategy())

	for _, h := range p.hints {
		fmt.Printf("cache hint: position=%s index=%d\n", h.Position, h.Index)
	}
}
