# go-llm-contracts

The contract layer between application code and concrete LLM provider
adapters: the `Provider` interface, optional capability extensions
(`RateLimited`, `Cacheable`, `CacheableProvider`, `ProviderWithUsage`), and
pure-algorithmic rate-budget helpers (`TokenRateTracker`, `CircuitBreaker`,
`PacingWait`). It contains no provider implementations and no network code —
adapters compose these without taking on a transport dependency.

## Start Here

- `README.md` and `doc.go` describe the layering and the quickstart.
- `provider.go` declares `Provider`; `cacheable.go`, `ratelimited.go` and
  `usage.go` declare the optional capability extensions.
- `ratelimit.go` owns `TokenRateTracker`, `PacingWait` and
  `ErrRequestExceedsRateBudget`; `circuit.go` owns `CircuitBreaker`.
- `cache.go` owns the default cache strategy; `reasoning.go` carries
  reasoning config through context.
- `examples/` has a runnable program per primitive.

## Commands

```bash
make vet
make test
```

`make lint` is an alias for `go vet ./...` — it runs no separate linter. There
is no CI workflow in this repo.

## Boundaries

Contracts and pure algorithms only. A concrete provider, an HTTP client or an
SDK dependency landing here would break the reason adapters can share this
module cheaply. `go-llm-types` is the one dependency, and it is types-only.

The rate-budget primitives are shared mutable state and are documented safe for
concurrent use. `TestTokenRateTracker_ConcurrentAccess` and
`TestCircuitBreaker_ConcurrentAccess` exist because adapters call these from
many in-flight requests; keep the race detector in every run.

`ErrRequestExceedsRateBudget` means waiting cannot help — the request is larger
than the whole window, so the caller must shrink it (compact history) rather
than retry. Turning it into a retryable error produces an infinite wait.

`TokenRateTracker` is a sliding window whose availability never goes negative
(`TestTokenRateTracker_Available_NeverNegative`), and the circuit breaker's
open state lets exactly one probe through after cooldown rather than
reopening the floodgates (`TestCircuitBreaker_HalfOpen`,
`TestCircuitBreaker_HalfOpenProbeFailure`).
