package llmcontracts

import (
	"context"

	"github.com/hollis-labs/go-llm-types"
)

// ProviderWithUsage is an optional extension interface for providers that can
// return token usage for non-streaming completions.
type ProviderWithUsage interface {
	Provider
	// CompleteWithUsage makes a non-streaming completion call and returns
	// any token usage the underlying provider surfaces.
	CompleteWithUsage(ctx context.Context, req llmtypes.ChatRequest) (llmtypes.CompleteResult, error)
}
