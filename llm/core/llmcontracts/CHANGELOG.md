# Changelog

## Unreleased

- `CacheHint` is now a type alias for `llmtypes.CacheHint`. Callers that use
  `llmcontracts.CacheHint` continue to compile unchanged.
- `CacheableProvider` and its `SetCacheHints` method are deprecated. Storing
  cache hints on the provider singleton races under concurrent callers and
  could drop `cache_control` markers, producing degenerate echoed turns
  with `cache_read=0` (agridd/Nanite FU-13). Set hints on the per-call
  request via `llmtypes.ChatRequest.CacheHints` instead.
- `Cacheable.EstimateCacheablePrefix` doc updated: implementations should
  read hints from `req.CacheHints`.
- `examples/cacheable` rewritten to demonstrate the per-call pattern.

Requires `go-llm-types` ≥ the release that adds `ChatRequest.CacheHints`.

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
