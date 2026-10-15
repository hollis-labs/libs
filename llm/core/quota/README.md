# quota

Usage limits for LLM and API calls: windows, counters, limits, and a
Check / Reserve / Commit flow with an injectable clock and pluggable stores.

```go
import "github.com/hollis-labs/substrate/llm-core/quota"
```

## The model

A `Limit` is a budget: at most `Max` of a `Unit` for a `Key` over a `Window`.

- **Units stay native.** `"input_tokens"`, `"requests"`, `"usd_micros"`: whatever
  the provider or the application counts in. Nothing is converted, and a limit
  only sees usage recorded in its own unit.
- **Windows come in three kinds, and they never share a counter.** A counter is
  `(key, unit, window ID)`, so a calendar-day limit and a rolling-24-hour limit
  on the same key count separately, and a provider's reported figure is never
  mixed into locally counted usage.

| Window | Built with | Counts |
|---|---|---|
| Calendar | `Calendar(Day \| Week \| Month, loc)` | from the start of the current local day, ISO week (Monday) or month in `loc` (nil = UTC) to its end |
| Rolling | `Rolling(d)` | usage younger than `d` |
| Provider | `Provider(name)` | the provider's own remaining figure and reset time, given with `Limiter.Observe`, minus usage recorded since |

**Daylight saving.** Calendar windows follow the local wall clock in their zone.
A day that crosses a DST change is 23 or 25 hours long and its limit is not
prorated. Where a zone skips local midnight, the day starts at the first instant
of the new date (01:00); where midnight occurs twice, at the first of them.

## The flow

```go
l := quota.New(store, quota.WithClock(clock)) // clock is optional

lim := quota.Limit{Key: "model=sonnet", Unit: "input_tokens", Window: quota.Rolling(time.Minute), Max: 400_000}

r, d, err := l.Reserve(ctx, lim, estimate) // check and hold the estimate
if err != nil { ... }
if !d.Allowed {
    // d.Reason is ReasonExhausted (wait d.RetryAfter) or ReasonTooLarge (waiting cannot help)
}
// ... send the request, stream the response ...
err = r.Commit(ctx, actual) // replace the estimate with what was really used
// or r.Cancel() if the request never ran
```

`Check(ctx, lim, amount)` answers the same question without holding anything.

A `Decision` is `{Allowed, Remaining, RetryAfter, Reason}`:

- `Remaining` is what is left before this request (committed usage and other
  open reservations subtracted). It never goes negative; it is `Unknown` (-1)
  only for a provider window without a usable snapshot.
- `RetryAfter` is set only when waiting helps (`ReasonExhausted`).
- `ReasonTooLarge` means the request is bigger than the whole limit; shrink it.
- `ReasonNoSnapshot` means a provider window has nothing to go on; the request
  is allowed.

Open reservations are kept by the `Limiter` in memory. Usage is recorded dated
when the reservation was made, so a request counts in the window it started in.

## Stores

The library never opens a database, never writes the application's events and
never deletes them. Counters derive from what the application already records,
and its budget history stays where it keeps it.

| Store | Use when |
|---|---|
| `MemoryStore` | one process, losing counts on restart is acceptable (tests, short-lived limits) |
| `SeededStore` | one process gates the calls and the application keeps its own log of them: seed from the log on start and after `Reseed`, count in memory in between |
| `SQLStore` | the application has an events table: every decision sums it through `database/sql`, with the application's `*sql.DB` |
| your own | implement `Store` (two methods) over a query you already have |

`MemoryStore` and `SeededStore` are `Recorder`s: `Commit` records the actual
amount in them. `SQLStore` is not: the application's event row is the record,
so write it before calling `Commit`.

`SQLStore` needs a table with a timestamp column and an integer amount column,
a `Filter` that maps a counter to a `WHERE` condition, and optionally a
`Placeholder` (`DollarPlaceholder` for PostgreSQL), `TimeArg` and `ScanTime`
for the driver's time representation. Table and column names must be plain
identifiers; anything else is refused.

`quotatest` is the conformance suite for stores: `RunStore` checks the `Store`
contract and `RunLimiter` drives a `Limiter` over the store with an injected
clock. Run both for a new store.

## Migrating

Each of these keeps working as it is; move when convenient.

### `llmcontracts.TokenRateTracker` (this module)

| Before | After |
|---|---|
| `NewTokenRateTracker(n)` | `Limit{Unit: "input_tokens", Window: Rolling(time.Minute), Max: n}` over a `MemoryStore` |
| `Record(tokens)` | `Reserve(ctx, lim, estimate)` before sending, `Commit(ctx, tokens)` after |
| `Available()`, `Remaining()` | `Check(ctx, lim, 0)`, then `.Remaining` |
| `WaitTime(n)` | `Check(ctx, lim, n)`, then `.RetryAfter` |
| `ErrRequestExceedsRateBudget` | `Decision.Reason == ReasonTooLarge` |
| `UpdateLimit(n)` from response headers | `Observe` on a `Provider(...)` limit with the reported remaining, limit and reset |

One behavior differs on purpose: `Record` counts a request whether or not it
fits, while `Reserve` holds nothing when the request is denied, so a caller
that keeps asking does not extend its own wait. `PacingWait` stays useful for
waiting out a `RetryAfter`.

### `clientguard.RateLimiter` (`libs`, `plugin-mcp/go-mcp/clientguard`)

`libs` may not depend on `substrate`, so `clientguard` itself does not move to
this package. An application that depends on both can replace a limiter it
holds:

| Before | After |
|---|---|
| `NewRateLimiter(limit, period)` | `Limit{Unit: "requests", Window: Rolling(period), Max: limit}` over a `MemoryStore` |
| `Allow()` | `Reserve(ctx, lim, 1)`; on `Allowed`, `Commit(ctx, 1)` |
| `Available()`, `WaitTime()` | `Check(ctx, lim, 1)`, then `.Remaining`, `.RetryAfter` |
| `Wait(ctx)` | loop: `Reserve`; if denied, wait `RetryAfter` or until `ctx` ends |
| `Reset()` | a new `MemoryStore` and `Limiter` |

### Cerberus rate limits (`cerberus`, `internal/cerbapi/rates.go`)

| Before | After |
|---|---|
| a rule's `rate: N/<window>` per principal and effect | `Limit{Key: <rule/principal/effect key>, Unit: "calls", Window: Rolling(<window>), Max: N}` |
| `hits` seeded from `audit.ReadRecords` | `NewSeededStore(seed, policy.MaxRateWindow, clock)`, where `seed` turns allowed outcome records into `SeedEntry`s |
| reseed when the set of rated rules changes | `Reseed()` |
| `hold` before the call, `release` if refused | `Reserve` before, `Cancel` if refused, `Commit(ctx, 1)` when it ran |
| `count`: hits in the window and when the oldest leaves | `Check(ctx, lim, 1)`: `.Remaining`, and `now + RetryAfter` for "the next is allowed at" |

The window semantics match: a call counts while it is strictly younger than
the window.

### Tether usage budgets (`tether`, `internal/llm/usagebudget`)

| Before | After |
|---|---|
| `budgetWindowStart` (and its copy in `internal/api/ai.go`): UTC day or month | `Calendar(Day, time.UTC)` or `Calendar(Month, time.UTC)`; `Bounds(now)` gives the same start, and the end |
| spend from `ai_events` via `QueryAIUsageSummary` | a `Store` over that query (its filter fields come from `BuildFilter`), or `SQLStore` over a view with an integer amount (for example cost in micro-dollars) |
| `spent >= limit`, `spent + estimate > limit` | `Check(ctx, lim, estimate)`; for a request in flight, `Reserve` then `Commit` |

A budget in another zone is now one argument (`Calendar(Month, loc)`) instead of
a code change.

## What it is not

- Not a circuit breaker or a cooldown. "When may we retry this account after an
  error" is a separate concern; a quota answers "how much remains".
- Not a cost calculator. It counts amounts it is given; `costcalc` prices usage.
- Not distributed. Open reservations live in one process. Several processes
  sharing a budget each see the others' committed usage only through a shared
  store (`SQLStore`, or a reseeded `SeededStore`).
