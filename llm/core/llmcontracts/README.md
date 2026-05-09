# go-llm-contracts

Shared interfaces, types, and rate-budget primitives for HTTP/SDK-backed LLM
providers in the Hollis Labs portfolio.

## Status

Alpha (`v0.1.x`). The surface may shift during the SP-20260508-0001 migration
window while nanite adopts SDK-backed Anthropic and OpenAI wrappers.

## What's Here

- Interfaces: `RateLimited`, `Cacheable`, `CacheableProvider`, `ProviderWithUsage`
- Interfaces: `Provider`
- Types: `CacheHint`, `ReasoningConfig`
- Helpers: `WithReasoningConfig`, `ReasoningConfigFromContext`, `DefaultCacheStrategy`
- Rate-budget primitives: `TokenRateTracker`, `CircuitBreaker`,
  `ErrRequestExceedsRateBudget`, `PacingWait`, `CircuitState`, `DefaultCooldown`

## Dependency Note

`go-llm-contracts` depends only on `github.com/hollis-labs/go-llm-types` for
the transport-agnostic request, stream, and usage types used by its interfaces.

## Intended Consumers

- `nanite` SDK-backed wrappers under `internal/llm/...`
- Any future Hollis Labs service that needs transport-agnostic HTTP/SDK LLM
  contracts and shared rate-budget primitives

## Provenance

This module restores interfaces deleted from `go-providers` in `v0.10.0` and
relocates the surviving rate-budget primitives that were HTTP-coupled in
practice.

Sprint references:

- `agent-workspaces/execution/nanite/sp-20260508-0001/2026-05-09/orchestrator-notes.md`
- `agent-workspaces/execution/nanite/sp-20260508-0001/2026-05-09/cw-spike-rate-tracker-verdict.md`
