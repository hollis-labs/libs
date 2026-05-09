# Changelog

## v0.1.0

Initial alpha release.

- Restored from the pre-`go-providers v0.10.0` HTTP contract surface:
  `RateLimited`, `ProviderWithUsage`, `Cacheable`, `CacheHint`,
  `CacheableProvider`, `ReasoningConfig`, `WithReasoningConfig`,
  `ReasoningConfigFromContext`, `DefaultCacheStrategy`, `Provider`
- Uses `go-llm-types` as the standalone home for shared request/response
  carrier types (`ChatRequest`, `StreamEvent`, `Usage`, `CompleteResult`, etc.)
- Relocated from `go-providers`: `TokenRateTracker`, `CircuitBreaker`,
  `ErrRequestExceedsRateBudget`, `PacingWait`, `CircuitState`, `DefaultCooldown`
