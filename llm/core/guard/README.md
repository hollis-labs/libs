# guard

`github.com/hollis-labs/substrate/llm-core/guard` decides whether a call to an
LLM resource may proceed, and learns from how each call ended. It keeps state
in memory, imports only the standard library, and takes a `Clock` everywhere so
tests can move time.

| Part | Answers |
|---|---|
| `CircuitBreaker` | Is the resource healthy? Trips after consecutive failures; after a cooldown admits exactly one probe. |
| `Classify`, `HTTPError`, `ParseRetryAfter` | What did this error mean, and did the provider say when to come back? |
| `Cooldown` | When may this key be tried again? |
| `Guard` | All of the above per `Key`, plus an optional `QuotaCheck`, as one `Decision`. |

## Using Guard

```go
g := guard.New(guard.Config{
	Breaker: guard.BreakerConfig{Threshold: 5, Cooldown: 30 * time.Second},
	Quota:   myQuotaCheck, // optional: "how much remains"
})
key := guard.Key{Resource: "provider-a", Account: credentialID, Model: model}

err := g.Do(ctx, key, func(ctx context.Context) error {
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return err // classified from the standard library: timeouts, resets, ...
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return guard.HTTPError(resp.StatusCode, resp.Header, readErrorBody(resp))
	}
	return decode(resp)
})
if errors.Is(err, guard.ErrRefused) {
	var re *guard.RefusedError
	errors.As(err, &re)
	// re.Decision.Reason, re.Decision.RetryAfter
}
```

`Guard.Check(key)` returns the same `Decision` without taking a probe slot; use
it to answer "when may we retry" without making a call. `Guard.Admit` and
`Ticket.Done` are the two halves of `Do`, for callers that cannot wrap the call
in a function (a stream that reports its outcome later). Every admitted
`Ticket` must end in exactly one `Done` or `Release`.

`Admit` checks, in order: the cooldown of the key, its account and its
resource; the `QuotaCheck`; then the breaker of the key's resource and account.
The breaker runs last because admitting the half-open probe is the only check
with a side effect. When the cooldown and the quota check both refuse, the
reason is the cooldown and `RetryAfter` is the longer of the two waits.

## What each class does

| Class | Typical source | Breaker | Cooldown |
|---|---|---|---|
| `ClassRequest` | 400, 404, 413, 422: a malformed or impossible request | success (the upstream answered) | none |
| `ClassQuota` | 429, an exhausted quota | success | the key, for the provider's retry-after or `QuotaCooldown` |
| `ClassAuth` | 401, 403 | success | the whole account (every model), for the retry-after or `AuthCooldown` |
| `ClassTransient` | 5xx, 529 overloaded, 408, a deadline | failure | the key, for the retry-after or the next `Backoff` delay |
| `ClassConnection` | refused dial, reset, unexpected EOF | failure | none |
| `ClassUnknown` | anything unclassified | failure | as transient |
| `ClassCanceled` | the caller canceled; a call another guard refused | no verdict; frees the probe slot | none |

A malformed request therefore never cools down a healthy account, and an auth
failure stops every model of that credential, not just the one asked for.
`HTTPError` maps only the status code. An adapter that reads the error body
(for example an "overloaded" type sent with a 4xx) can set `Class` on the
returned `*Error`. Error types can also classify themselves by implementing
`GuardClass() Class` and `RetryAfter() time.Duration`.

`Cooldown` answers "when may we retry", not "how much remains". A quota source
plugs in through `Config.Quota`. A reset the provider reports out of band, such
as a quota window's reset time, goes in with `Cooldown.Set`. A new cooldown
never shortens one already in force, and a success ends the cooldown of its key
and account but not of the whole resource.

## The breaker

The breaker trips after `Threshold` consecutive failures and refuses every call
for `Cooldown`. After that, the first caller becomes the half-open probe and
every other caller is refused until the probe reports: its success closes the
circuit, and its failure re-opens it and restarts the cooldown. A probe that
never reports holds the slot for at most `ProbeTimeout` (default: `Cooldown`).

`Admit` returns an `Admission` carrying an epoch. Its result counts only if the
breaker has not changed state since, so a slow call admitted before a trip
cannot close the circuit when it finally returns, and a `Reset` cannot be
undone by an in-flight probe. `Allow` with `RecordSuccess`, `RecordFailure` and
`Release` is the admission-less form. Those calls act on the current state, so
prefer `Admit` when a call can outlive a state change.

`Guard` keeps one breaker per resource and account, not per model: connection
and server failures are not model-specific, and one credential can be healthy
while another is not.

## Moving to guard

- **`llmcontracts.CircuitBreaker`** keeps its API and now runs on
  `guard.CircuitBreaker`. Its half-open state used to admit every caller that
  asked. It now admits one probe and refuses the rest until the probe reports or
  one cooldown passes. It is deprecated: replace `IsOpen` / `RecordFailure` /
  `RecordSuccess` with `Guard.Do`, or with `CircuitBreaker.Admit` and the
  returned `Admission`. Its default threshold stays 3; `guard`'s is 5.
- **`clientguard`** in the `libs` repository (`plugin-mcp/go-mcp/clientguard`)
  has the same breaker algorithm, which this package started from. It stays
  where it is: `libs` may not import `substrate`.
- **Retry loops with exponential backoff and jitter** can take their delays from
  `Backoff.Delay`. If the retry decision belongs to a resource rather than to
  one loop, use `Cooldown`.
- **Policy-denial suspensions** that a person must reset, such as Cerberus's
  session breaker, are not a provider-health breaker and are not covered here:
  there is no automatic half-open and no cooldown to wait out.
