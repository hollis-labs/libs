package llmcontracts

// RateLimited is implemented by providers that track an input-tokens-per-minute
// rate limit (e.g. Anthropic). Callers use this for observability — surfacing
// the live calibrated limit in telemetry. Providers without a rate tracker do
// not implement this interface; callers type-assert.
type RateLimited interface {
	// RateLimitTPM returns the current input-tokens-per-minute limit observed
	// from the most recent provider response, or 0 when no calibration has
	// happened yet. Implementations must not return seeded/default values
	// before the first real response — 0 means "unknown", and callers may
	// use it to skip emitting limit telemetry rather than mislabel a guess.
	RateLimitTPM() int
}
