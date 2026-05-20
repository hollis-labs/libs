package llmcontracts

import "github.com/hollis-labs/go-llm-types"

// CacheHint aliases llmtypes.CacheHint so callers that already import this
// package keep working. Authoritative definition lives in go-llm-types
// alongside llmtypes.ChatRequest.CacheHints.
type CacheHint = llmtypes.CacheHint

// CacheableProvider was implemented by providers that supported prompt
// caching. The chat engine called SetCacheHints before each request to tell
// the provider WHAT to cache.
//
// Deprecated: storing hints on the provider mutates shared state and races
// under concurrent callers — one session's SetCacheHints can wipe another's
// hints between assignment and read, producing requests without
// cache_control markers and degenerate echoed turns. Set hints on the
// per-call request via llmtypes.ChatRequest.CacheHints instead. Providers
// should read req.CacheHints when building each request.
type CacheableProvider interface {
	// Deprecated: see CacheableProvider. Use llmtypes.ChatRequest.CacheHints.
	SetCacheHints(hints []CacheHint)
}

// DefaultCacheStrategy returns the standard set of cache hints to attach to
// each request's llmtypes.ChatRequest.CacheHints. It caches:
//   - The system prompt
//   - The last tool definition (marking the end of the tools block)
//   - The last 2 user messages (most recent conversation context)
func DefaultCacheStrategy() []CacheHint {
	return []CacheHint{
		{Position: "system", Index: 0},
		{Position: "tools", Index: 0},
		{Position: "recent_message", Index: 0},
		{Position: "recent_message", Index: 1},
	}
}
