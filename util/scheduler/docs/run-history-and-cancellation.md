# Run history and cancellation design

The current durable unit is a fire. A fire has a nominal occurrence identity,
attempt count, current claim epoch, retry deadline, final error and policy
reason. It is not a process handle or a log store. Runner success means dispatch
acceptance, not successful completion of an external job. The changes described
below are a future design, not implemented APIs.

## Attempt history

An application can append an attempt record keyed by
`(FireID, Attempt, ClaimedAt)`. Include dispatch start/finish timestamps,
acceptance outcome, a bounded sanitized cause, and an opaque run/log reference.
ClaimedAt distinguishes recovery of an ambiguous attempt without resetting its
retry budget. Keep payloads, process output and credentials out of the library's
history: applications resolve their own log references and permissions.

History insertion and the fenced fire transition should share a transaction
in the application's store. A losing stale owner must not append an authoritative
completion. A durable outbox is preferable when log/telemetry publication uses
another service; observers alone cannot promise delivery. Retention needs an
explicit application policy for attempt/log records alongside the fire prune
fences. Pruning terminal fire rows must not reset deduplication or erase active
attempt obligations.

## Cancellation

For an in-process Enqueue call, a future cancellation registry could map a
claimed epoch to its cancel function. Cancellation must address the exact
FireID/Attempt/ClaimedAt and reject a stale epoch. Cross-process cancellation
requires a durable request and an engine polling/notification mechanism;
mutating an in-memory map is insufficient.

Pausing a schedule should stop future materialization. Deleting a schedule
should retain already-materialized fires and their identity fences. Neither
operation currently promises cancellation of accepted jobs. A future API should
make cancel-on-pause/delete explicit and default to preserving accepted work.
Applications must separately cancel externally accepted jobs through their
runner/backend, persist the outcome, and distinguish cancellation requested
from cancellation confirmed. A library context deadline cannot establish that
an external process stopped.

`Stop` currently stops new admissions and drains Enqueue calls. Per-fire contexts
bound cooperative dispatch; outcome persistence survives caller cancellation.
Changing shutdown to abandon accepted work or adding a durable cancelled fire
state requires a new Store/conformance contract. No signal handling,
process-group behavior, live scheduler activation or consumer adoption is
included in this source change.
