package llmcontracts

import (
	"context"

	"github.com/hollis-labs/go-llm-types"
)

// Provider is the interface for LLM provider adapters.
type Provider interface {
	// StreamChat streams a response for the given request. Tools are optional.
	StreamChat(ctx context.Context, req llmtypes.ChatRequest) (<-chan llmtypes.StreamEvent, error)
	// Complete makes a non-streaming completion call.
	Complete(ctx context.Context, req llmtypes.ChatRequest) (string, error)
	// Capabilities returns the capabilities supported by this provider.
	Capabilities() llmtypes.ProviderCapabilities
}
