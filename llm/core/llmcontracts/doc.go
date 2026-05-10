// Package llmcontracts provides transport-agnostic contracts for LLM provider
// adapters: the core Provider interface, optional capability extensions
// (RateLimited, Cacheable, CacheableProvider, ProviderWithUsage), shared types
// (CacheHint, ReasoningConfig), sentinel errors, and pure-algorithmic
// rate-budget helpers (TokenRateTracker, CircuitBreaker, PacingWait).
//
// The package contains no provider implementations and no network code.
// Concrete adapters live in separate packages and depend on this one for the
// shared interface and rate-budget primitives.
package llmcontracts
