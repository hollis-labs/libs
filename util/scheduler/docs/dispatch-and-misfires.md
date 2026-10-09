# Dispatch and missed occurrences

The engine reserves worker capacity before calling `Runner.Enqueue`. The
default is four concurrent calls; `WithConcurrency(n)` changes the bound. The
background loop continues materializing and admitting other schedules while a
call is in progress. A full pool leaves fires in the store for a later tick;
there is no unbounded goroutine queue. `TickNow` waits for the calls it admits,
while `Stop` stops admission and drains admitted calls.

`WithFireTimeout` sets the context deadline passed to each Enqueue call
(default 30 seconds). Runners must honor that context. Go cannot force a
function that ignores its context to return, and the engine does not abandon
such a function and spawn replacements. A context-ignoring runner can occupy
a worker and delay shutdown. The timeout applies to dispatch acceptance, not
the runtime of a job accepted by an application's external queue.

Claims use the clock at worker admission, including after a capacity wait.
The effective claim lease is at least the configured fire timeout plus one
second. Fire IDs always use the nominal scheduled time, never admission time.
Recovery of an expired claim keeps the same attempt and changes its claim
epoch; a stale owner cannot commit an outcome. Runners still need durable
idempotency by FireID to cover ambiguous process failures and redelivery.

## Overlap

`Schedule.Overlap` is copied into each fire. An empty policy means `OverlapSkip`:

| Policy | Another occurrence holds an unexpired claim |
| --- | --- |
| `OverlapSkip` | Record the new fire as terminal skipped with `overlap_skip`. |
| `OverlapQueue` | Keep the fire pending until the active claim finishes or expires. |
| `OverlapAllow` | Admit the different occurrence concurrently, within worker capacity. |

Overlap concerns concurrent Enqueue calls. It does not infer whether an
accepted external job is still running. Even `OverlapAllow` preserves the
per-fire compare-and-swap and never steals an unexpired claim of the same fire.
The store must atomically check sibling claims across engine instances.

Queued pending/retrying occurrences are bounded by `MaxQueuedFires` (default
100). The store refuses an excess pending creation with `ErrQueueFull`; the
engine materializes that same occurrence as skipped with `queue_full` through
the original schedule compare-and-swap. Due-fire queries exclude queued
occurrences blocked by an active sibling, preventing them from filling a batch
and starving unrelated schedules. Queued work is durable across restarts.

## Misfires

An occurrence is stale when observed later than `Schedule.MisfireGrace` after
its nominal time. Zero grace selects the one-minute default. Negative bounds
and unknown policies are rejected. An empty policy means `MisfireRunOnce`,
preserving the earlier coalescing behavior and original first-missed FireID.

| Policy | Late occurrence behavior |
| --- | --- |
| `MisfireSkip` | Persist a skipped occurrence and advance past the observed time. |
| `MisfireRunOnce` | Dispatch once and advance past the observed time. |
| `MisfireRunAll` | Materialize nominal occurrences in order, bounded by `MaxCatchUp`. |

Run-all defaults to 100 catch-up occurrences and is also capped by the engine's
due-batch limit. Excess occurrences become one skipped history record with
`catchup_limit`; its `CoalescedThrough` records the end of the omitted span.
Skip/run-once likewise retain that span in `CoalescedThrough`. These aggregates
do not claim to enumerate every omitted occurrence. Run-all is still subject
to the chosen overlap policy and queue bounds; choose queue or allow when each
eligible catch-up dispatch must be retained. Interval and zoned-cron recurrence
use the timing helpers, including their DST policy, independently of jitter.

`ObserverMisfire` follows a successful materialization compare-and-swap, with
fixed reasons `misfire_skip`, `misfire_coalesced`, `misfire_run_all`, or
`catchup_limit`. `MisfireCount` counts represented decisions/records, not an
estimated number of occurrences in an aggregated span. Skipped decisions also
emit `ObserverSkip`. Losing engines cannot report a winning misfire decision.
Each actual failed Enqueue that commits retry/exhaustion emits one
`ObserverFailure`, carrying its durable outcome snapshot. Duration measures
the Enqueue call and lag measures actual dispatch start minus scheduled time;
both are nonnegative. Observer failures remain passive.

## SQLite reference store

`sqlstore.New(db, options...)` enables WAL on a file database. SQLite in-memory
databases retain memory journal mode. Every write obtains a connection and
applies a five-second busy timeout, including connections created later by the
caller's pool. `WithBusyTimeout(d)` overrides it; zero disables waiting and
negative or overflowing values fail construction. This overwrites the supplied
handle's previous write-connection busy setting; choose an explicit option to
match the application's policy. The store does not resize or close that pool.

Run `Migrate` before constructing a store. Revision 003 adds separate JSON
metadata tables for schedule options and fire policy/history, preserving the
original reference tables; migrations remain idempotent. Legacy records with
no metadata use defaults. Applications with their own Store need equivalent
durable policy fields, atomic queue/overlap checks, and skipped history. See
`MIGRATION.md` for the complete persistence contract.
