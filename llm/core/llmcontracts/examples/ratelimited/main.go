// Package main demonstrates how to expose an observed per-minute token rate
// limit through the llmcontracts.RateLimited optional interface, and how a
// caller can type-assert to surface it for telemetry.
package main

import (
	"context"
	"fmt"

	llmcontracts "github.com/hollis-labs/go-llm-contracts"
	llmtypes "github.com/hollis-labs/go-llm-types"
)

// rateLimitedProvider augments a basic Provider with a calibrated input
// tokens-per-minute observation. Real adapters update observedTPM from the
// most recent provider response (e.g. a rate-limit response header).
type rateLimitedProvider struct {
	observedTPM int
}

func (p rateLimitedProvider) StreamChat(context.Context, llmtypes.ChatRequest) (<-chan llmtypes.StreamEvent, error) {
	ch := make(chan llmtypes.StreamEvent)
	close(ch)
	return ch, nil
}
func (p rateLimitedProvider) Complete(context.Context, llmtypes.ChatRequest) (string, error) {
	return "", nil
}
func (p rateLimitedProvider) Capabilities() llmtypes.ProviderCapabilities {
	return llmtypes.ProviderCapabilities{}
}

// RateLimitTPM reports the most recently observed limit. Returning 0 signals
// "unknown" — callers should skip emitting limit telemetry rather than
// publish a guess.
func (p rateLimitedProvider) RateLimitTPM() int { return p.observedTPM }

// Compile-time checks.
var (
	_ llmcontracts.Provider    = rateLimitedProvider{}
	_ llmcontracts.RateLimited = rateLimitedProvider{}
)

func main() {
	var p llmcontracts.Provider = rateLimitedProvider{observedTPM: 40_000}

	if rl, ok := p.(llmcontracts.RateLimited); ok {
		if tpm := rl.RateLimitTPM(); tpm > 0 {
			fmt.Printf("provider observed limit: %d input tokens/min\n", tpm)
		} else {
			fmt.Println("provider observed limit: unknown (skipping telemetry)")
		}
	} else {
		fmt.Println("provider does not expose a rate limit")
	}
}
