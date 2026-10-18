# go-modelsdev

`go-modelsdev` is a Go library for consuming the [models.dev](https://models.dev) API. It provides a cached client that returns LLM provider and model metadata — pricing, context limits, modalities, and capabilities — for use in services that need to look up model info at runtime.

[![Go Reference](https://pkg.go.dev/badge/github.com/hollis-labs/libs/llm/core/modelsdev.svg)](https://pkg.go.dev/github.com/hollis-labs/libs/llm/core/modelsdev)

## Status

Pre-1.0 (v0.x.y). The exported API surface is small and intended to remain stable, but breaking changes can still land in minor bumps before v1.0 — see [module changelog](../CHANGELOG.md) for each release's contract.

## Install

```bash
go get github.com/hollis-labs/libs/llm/core/modelsdev
```

## Quick Usage

### Look up a single model

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/hollis-labs/libs/llm/core/modelsdev"
)

func main() {
    c := modelsdev.New()

    if err := c.Refresh(context.Background()); err != nil {
        log.Fatal(err)
    }

    m, ok := c.Get("anthropic", "claude-sonnet-4-5")
    if !ok {
        fmt.Println("model not found")
        return
    }
    fmt.Printf("%s — input: $%.2f/M tokens (unknown=%t)\n", m.Name, m.Cost.Input, m.Cost.UnknownRates.Input)
}
```

### Run the background refresher

The refresher keeps the cache fresh without blocking your application startup:

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

c := modelsdev.New()
c.StartRefresher(ctx) // returns immediately; refreshes in background

// c.Get, c.List, etc. are safe to call immediately (reads from disk cache
// if the in-memory catalog is not yet populated).
```

`StartRefresher` gives you nothing to wait on. When shutdown must know the refresher has stopped — before closing a store your `WithOnRefresh` callback writes to, say — run the loop yourself with `Run`, which blocks until ctx is cancelled and returns only once no refresh is in flight:

```go
done := make(chan struct{})
go func() {
    defer close(done)
    c.Run(ctx)
}()

// on shutdown
cancel()
<-done
```

Use `WithOnRefresh` to be notified when the catalog has been updated — useful for pushing data into your own registry without polling:

```go
c := modelsdev.New(modelsdev.WithOnRefresh(func(c *modelsdev.Client) {
    log.Printf("catalog refreshed: %d models", len(c.List()))
}))
c.StartRefresher(ctx)
```

### List all models across all providers

```go
for _, ref := range c.List() {
    fmt.Printf("%s/%s — context window: %d\n",
        ref.ProviderID, ref.ID, ref.Limit.ContextWindow)
}
```

### Runnable examples

See [`examples/`](examples/) — each example is its own `main` package that runs end-to-end against the live API:

- [`examples/lookup`](examples/lookup) — one-shot fetch + `Get` lookup.
- [`examples/refresher`](examples/refresher) — `StartRefresher` + `WithOnRefresh` hook, prints a catalog summary.

From the `llm/core` module directory:

```bash
go run ./modelsdev/examples/lookup
go run ./modelsdev/examples/refresher
```

## Cache Behavior

- Default cache directory: `~/.cache/go-modelsdev/` (via `os.UserCacheDir()`)
- Cache files: `catalog-v2-<sha256-of-source-URL>.json` (schema and source bound)
- Default TTL: 24 hours
- **Atomic write contract**: the cache is written to a unique temporary file first, then renamed into place. If a fetch or write fails, the existing cache remains intact and the client continues serving stale-but-valid data.

## Options

| Option | Default | Description |
|---|---|---|
| `WithCacheDir(dir string)` | `~/.cache/go-modelsdev/` | Override the cache directory |
| `WithCacheTTL(d time.Duration)` | `24h` | How long a cached catalog is considered fresh |
| `WithHTTPClient(c *http.Client)` | 30s-timeout client | Replace the HTTP client (useful for tests) |
| `WithURL(url string)` | `https://models.dev/api.json?type=all` | Override the API endpoint |
| `WithOnRefresh(fn func(*Client))` | nil | Callback fired after every successful refresh; reads allowed, no recursive Refresh/Run |
| `WithRefreshTimeout(d)` | `30s` | Bounds gate wait and HTTP attempts; callbacks must finish themselves |
| `WithMaxResponseBytes(n)` | `64 MiB` | Bound HTTP bodies and encoded cache; nonpositive or above 1 GiB uses default |
| `WithRetryBackoff(min, max)` | `1s`, `1m` | HTTP retry and background failure delays; invalid minimum uses 1s, max is at least min |
| `WithLegacyCacheSource(url)` | disabled | Explicitly attest which source wrote an old unbound cache |

## Dependencies

- [`github.com/cenkalti/backoff/v5`](https://github.com/cenkalti/backoff) — exponential backoff for fetch retries (3 attempts).
- [`github.com/stretchr/testify`](https://github.com/stretchr/testify) — test assertions (test only).

## Testing

```bash
go test -race ./...
```

Tests use `httptest.NewServer` and `t.TempDir()` — no network access or real API keys required.

## License

[MIT](LICENSE) © Hollis Labs.

## Metadata and price availability

The default endpoint includes specialized model types (`type=all`). `WithURL`
uses the supplied URL unchanged. `Model` and `ModelRef` retain canonical identity,
provider request metadata, model type/status, reasoning controls, input limits,
structured output, audio rates and context pricing tiers. Provider IDs and prices
remain separate even when offerings share a `CanonicalModelID`. Provider bodies,
headers and API URLs are descriptive data; this client never applies them to
inference requests.

`Capabilities.Unknown` distinguishes absent flags from explicit false. Missing
input limits are nil. `Pricing.UnknownRates` distinguishes omitted/null core
rates from explicit zero; audio rates are pointers with the same distinction.
These distinctions survive JSON and disk-cache round trips. The units are
`PricingCurrency` (USD) and `PricingUnit` (per million provider-native tokens).

The added tier slice makes `Pricing` non-comparable with `==`. Existing keyed
numeric Go literals keep their meaning: `Pricing{}` describes known
zero core rates. For incomplete programmatic data, start with `UnknownPricing()`
and clear each corresponding unknown flag when assigning its rate. Do not read
a numeric rate as free without checking its unknown flag. Likewise, manually
constructed capability flags remain known unless marked unknown.

Cache schema 2 preserves availability and binds each snapshot to the SHA-256 of
its exact source URL. Changing the URL (including filters) cannot load another
source's cache. URL values are not written to filenames or exposed in health.
Unbound schema 0/1 `catalog.json` files are ignored unless the caller explicitly
attests their source with `WithLegacyCacheSource(url)`, equal to the configured
URL. Do not automatically bind an old file whose origin is unknown. Bound legacy
caches remain usable offline and trigger refresh; schema 0 zero/false ambiguity
is treated conservatively as unknown. A successful fetch writes a schema 2 file.
Historical ledger snapshots are unchanged.

`Health()` reports empty/fresh/stale data independently of the last refresh
result, plus active-loop/in-flight state, attempts, consecutive failures, fetch
and retry times, safe error classes and HTTP status. Errors never include response
bodies, credentials, source URLs or filesystem paths. Health is process-local;
only the source snapshot and its fetch time persist across restarts. A nil/empty
next-attempt timestamp means a refresh is immediately eligible; scheduling only
runs while `Run` is active.

Each client serializes refreshes and callbacks. Failed cycles retry from 1s up to
1m by default, even when cached data has expired. Each cycle has at most three
HTTP attempts; permanent HTTP errors and invalid payloads are not retried within
the cycle. Empty catalogs, inconsistent IDs, negative limits, malformed/trailing
JSON, oversized bodies and cache write failures preserve last-good data. Startup
reads the matching cache before attempting a refresh. Reads expose read-only
metadata views; callers must not mutate returned maps, slices or pointers.

`GetWithFetchedAt` reads model data and its fetch timestamp together.
`pricesource.FromModelsDev` uses this operation to prevent mismatched price
provenance during concurrent refreshes. `LastFetchedAt` also hydrates disk state
on its first call. Unique temporary files isolate concurrent writers; refresh
ordering is guaranteed within one client, not across processes sharing a cache.

The catalog preserves audio and tiered prices without calculating them. The
five-component `costcalc` projection marks context-dependent rates unknown when
no tier has been selected; audio usage in `Usage.Dims` remains unpriced. Consumers
must check partial costs before displaying a complete estimate or enforcing a
budget. A frozen `pricesource.Table` contains this conservative projection, not
the full tier/audio metadata; keep the model catalog when those fields are needed.

Upstream contract reviewed 2026-10-18:
[models.dev schema](https://github.com/anomalyco/models.dev/blob/dev/packages/core/src/schema.ts)
and [API documentation](https://github.com/anomalyco/models.dev/blob/dev/README.md).
Tests use a frozen representative fixture with synthetic prices, not current
billing rates. See the module [changelog](../CHANGELOG.md) for release status.
