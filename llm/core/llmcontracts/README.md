# go-llm-contracts

Transport-agnostic interfaces, types, and rate-budget primitives for LLM
provider adapters.

`go-llm-contracts` is the contract layer between application code and concrete
HTTP/SDK-backed LLM provider wrappers. It defines the `Provider` interface
and a small set of optional capability extensions (`RateLimited`,
`Cacheable`, `CacheableProvider`, `ProviderWithUsage`), plus pure-algorithmic
rate-budget helpers (`TokenRateTracker`, `CircuitBreaker`, `PacingWait`) that
adapters can compose without taking on a transport dependency.

The package contains no provider implementations and no network code.

## Status

Pre-1.0 (`v0.x`). The surface may evolve until consumers settle.

## Install

```
go get github.com/hollis-labs/go-llm-contracts
```

Documentation: <https://pkg.go.dev/github.com/hollis-labs/go-llm-contracts>

## Quickstart

```go
package main

import (
    "context"
    "fmt"

    llmcontracts "github.com/hollis-labs/go-llm-contracts"
    llmtypes "github.com/hollis-labs/go-llm-types"
)

// myProvider is a stand-in for a real adapter.
type myProvider struct{}

func (myProvider) StreamChat(context.Context, llmtypes.ChatRequest) (<-chan llmtypes.StreamEvent, error) {
    ch := make(chan llmtypes.StreamEvent)
    close(ch)
    return ch, nil
}
func (myProvider) Complete(context.Context, llmtypes.ChatRequest) (string, error) { return "", nil }
func (myProvider) Capabilities() llmtypes.ProviderCapabilities                    { return llmtypes.ProviderCapabilities{} }

func main() {
    var p llmcontracts.Provider = myProvider{}
    fmt.Printf("provider capabilities: %+v\n", p.Capabilities())
}
```

Runnable examples for each major surface live under [`examples/`](examples/):

- [`examples/provider`](examples/provider) — implementing `Provider`
- [`examples/ratelimited`](examples/ratelimited) — implementing `RateLimited`
- [`examples/cacheable`](examples/cacheable) — implementing `CacheableProvider`
- [`examples/tokenratetracker`](examples/tokenratetracker) — using `TokenRateTracker`
- [`examples/circuitbreaker`](examples/circuitbreaker) — using `CircuitBreaker`

## What's Here

- Interfaces: `Provider`, `RateLimited`, `Cacheable`, `CacheableProvider`,
  `ProviderWithUsage`
- Types: `CacheHint`, `ReasoningConfig`
- Helpers: `WithReasoningConfig`, `ReasoningConfigFromContext`,
  `DefaultCacheStrategy`
- Rate-budget primitives: `TokenRateTracker`, `CircuitBreaker`,
  `ErrRequestExceedsRateBudget`, `PacingWait`, `CircuitState`,
  `DefaultCooldown`

## Dependencies

`go-llm-contracts` depends only on
[`github.com/hollis-labs/go-llm-types`](https://github.com/hollis-labs/go-llm-types)
for the transport-agnostic request, stream, and usage types referenced by its
interfaces.

## Contributing

Issues and pull requests are welcome. Please keep changes focused on the
contract surface and rate-budget helpers — provider implementations belong in
separate adapter packages.

## License

MIT — see [LICENSE](LICENSE).
