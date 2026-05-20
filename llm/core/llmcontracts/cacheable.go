package llmcontracts

import (
	"context"

	"github.com/hollis-labs/go-llm-types"
)

// Cacheable is implemented by providers that can estimate the cacheable prefix
// length of a request before sending it. Callers use this for observability —
// surfacing the prefix size in request_build telemetry alongside per-slot token
// estimates. Providers without prompt caching do not implement this interface;
// callers type-assert.
//
// The estimate is approximate: it reflects the same heuristic the provider
// uses internally for the rate-budget pre-flight (offset of the last
// cache_control marker after JSON marshalling, divided by 4 for tokens). It
// over-counts when markers land inside trailing tools or messages, but it is
// directionally correct and matches what the provider actually treats as
// cacheable. Authoritative cache hit/miss data comes from the provider's
// usage response; this is for pre-flight observability only.
type Cacheable interface {
	// EstimateCacheablePrefix returns the approximate token count of the
	// cacheable prefix for the given request, using the hints carried on
	// req.CacheHints. Returns 0 when no cache hints are configured or the
	// request would emit no cache_control markers.
	EstimateCacheablePrefix(ctx context.Context, req llmtypes.ChatRequest) int
}
