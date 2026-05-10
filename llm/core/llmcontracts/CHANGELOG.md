# Changelog

## v0.2.0 — 2026-05-10

- Added `examples/` directory with runnable programs covering each major
  surface: `Provider`, `RateLimited`, `CacheableProvider`, `TokenRateTracker`,
  and `CircuitBreaker`.
- Documentation pass on `README.md` and `doc.go`: clearer one-paragraph
  description, install snippet, godoc link, public-facing language only.
- No behavioural or API changes.

## v0.1.0

Initial alpha release.

- Restored from the pre-`go-providers v0.10.0` HTTP contract surface:
  `RateLimited`, `ProviderWithUsage`, `Cacheable`, `CacheHint`,
  `CacheableProvider`, `ReasoningConfig`, `WithReasoningConfig`,
  `ReasoningConfigFromContext`, `DefaultCacheStrategy`, `Provider`
- Uses `go-llm-types` as the standalone home for shared request/response
  carrier types (`ChatRequest`, `StreamEvent`, `Usage`, `CompleteResult`, etc.)
- Relocated rate-budget primitives: `TokenRateTracker`, `CircuitBreaker`,
  `ErrRequestExceedsRateBudget`, `PacingWait`, `CircuitState`, `DefaultCooldown`
