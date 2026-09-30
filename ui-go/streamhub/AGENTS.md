# go-streamhub

Payload-opaque, stdlib-only replayable fan-out hub: per-stream cursor, gap signal, replay ring and slow-consumer policies.

It is not an event model, a wire encoder or a database. A competent-looking change that teaches the hub what a payload means (a terminal event type, a frame shape, a SQL schema, presence or takeover) is the mistake this repo attracts: those belong to the application, which supplies a predicate, a `Filter` or a `Log`.

## Start Here

- `doc.go` — the package documentation: model, Subscribe algorithm, gaps, policies, terminal records.
- `hub.go` — `Hub`, `stream`, `Publish`/`Close`/`Shutdown`, the lock order (written at the `stream` type), the retention grace and `forget`.
- `subscription.go` — `Subscription`, the replay goroutine, `offer` (fan-out side), `Next`, gap markers.
- `policy.go` — `SlowPolicy` and its loss guarantees. `log.go`, `memorylog.go`, `types.go` — the `Log` seam, the in-memory log, values and errors.
- `hubtest/` — the acceptance suite: `Conformance` (a `Log`), `HubSuite` (the hub on any `Log`), `GatedLog` and read helpers. Backends outside this repo call these.
- `example_test.go` — the runnable example; the README `## Usage` fence must stay identical to `Example` (same program, `main` wrapped around it). There is no `examples/` directory.
- `.github/workflows/check.yml` — the full CI gate; `release.yml` refuses a tag with no CHANGELOG heading.

## Commands

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
go test -race -count=20 -run Concurrency ./...   # the concurrency suite; run it after touching hub.go or subscription.go
golangci-lint run --allow-parallel-runners
go test -run xxx -bench PublishFanout ./...
```

Run Go with `GOWORK=off`. There is no `go.work` and there must not be one.

CI (`.github/workflows/check.yml`) is the full gate.

## Boundaries

Each invariant below has a test that fails when it breaks.

- **Never hold a stream lock across `Log.After`.** Replay runs in its own goroutine, outside `stream.mu` and `pubMu`; `Subscribe` only reads the head under the lock. Guard: `hubtest` `HubSuite/ReplayDoesNotHoldStreamLock` (a `GatedLog` parks `After`, and `Publish`, `Subscribe` and `Close` must still complete). The only `Log` call under a hub lock is `Append`, under `pubMu`, and that is what orders records; `stream.mu` is never held across any `Log` call. `HubSuite/SlowAppendDoesNotBlockSubscribe` guards the second half.
- **Dedupe by Seq at the replay/live boundary.** The head is read at registration; live records above it queue; anything at or below what replay delivered is dropped in `popLocked` (`replayedThrough`). Replay stops at the head. Guards: `HubSuite/ContiguousUnderConcurrentPublish` (1,000 iterations, `-race`), `TestTether_NoBoundaryGap`, `TestNanite_LiveAfterReplay`.
- **Admission is closed under the same mutex as the subscriber set.** Subscribers live in `stream.subs` under `stream.mu`; `Subscribe` checks `gone`/`down` there, and `Shutdown` sets `down` there. There is no channel that fan-out sends on and someone else closes: delivery is a mutex-guarded queue, so there is no send-on-closed and no `recover()`. Guards: `TestNanite_NoRaceOnTakeoverDuringFanout`, `TestConcurrency_ChurnNoPanicNoLeak`.
- **`Publish` serialises `Append` plus fan-out per stream** (`pubMu`), and `stream.latest` is bumped in the same critical section as the fan-out. Do not split them: two publishers interleaving would let a subscriber see the head before a record it is about to receive live (the `go-runtime-events` `EmitReturning` failure). Guards: `HubSuite/ContiguousUnderConcurrentPublish`, `hubtest.Conformance/ConcurrentAppendsAreGapFree`.
- **Lock order:** `pubMu` -> `stream.mu` -> `subscription.mu`. A subscription never takes `stream.mu` while holding its own `mu` (`closeWith` releases first). `offer` never blocks: the `Block` policy returns `blocked` and `Publish` waits in `pushBlocking` after releasing `stream.mu`.
- **A `Log` never returns partial results.** `After` yields a `*GapError` (or a wrapped `ErrGap`/`ErrCursorAhead`) before any record. Guard: `hubtest.Conformance` (`TrimGapNeverPartial`, `CursorAhead`).
- **Lossy policies never lose silently.** `DropNewest`, `DropOldest` and `EvictAfterN` leave a `GapDropped` marker in the queue, in stream order, with an exact `Missed`. `EvictAfterN` closes on exactly the n-th consecutive drop (a drop-free accept resets it). Guards: `HubSuite/Policies/*`, `TestDropPoliciesConserveRecords`.
- **`CloseAndResume` must leave a resume cursor with no hole below it.** If it fires while replay is still running, the queued live records are discarded and `LastDelivered` is the last replayed record. Guard: `TestSlowCloseDuringReplayResumesWithoutLoss`.
- **A terminal record is last.** After it `Publish` returns `ErrTerminated`; EOF is only returned once everything up to and including it was delivered (including a `Block` publisher's parked push). Guards: `HubSuite/Terminal/*`.
- **Forgetting an ended stream waits for running replays** and keeps the map entry until the `Log` has let go. Guard: `TestForgetWaitsForRunningReplay`, `TestEndedStreamIsRetainedThenForgotten`.
- **No timing in tests without synctest.** Anything that depends on time runs inside `synctest.Test` (grace timers are `time.AfterFunc` inside the hub and are virtualised). Real-time timeouts in tests are failure nets only. A churn test must never sleep zero (virtual time would not advance).
- **Clean break.** No aliases, no `Deprecated:` shims, no re-exports.
- No `replace` directive in `go.mod` and no committed `go.work`: consumers cannot resolve either.
- Stdlib only. Do not add SQLite, an event model, or an encoder to this module; `go list -deps ./...` must show only the standard library.
- Never tag, push or create a remote without being asked.
