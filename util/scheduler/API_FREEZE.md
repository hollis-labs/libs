# Scheduler v1 API freeze plan

Status: proposed freeze checklist after the `util/v0.4.0` hardening release.
This document does not freeze an API or authorize `util/v1.0.0` publication.
The package is `github.com/hollis-labs/libs/util/scheduler`; it has no separate
module, version or release tag. The former standalone scheduler tags remain
historical and immutable.

## v0.4 release boundary

Release the consolidated module as `util/v0.4.0` only after retention, time-zone,
interval/jitter, overlap/concurrency, misfire, SQL-store and observation changes
are merged and verified together. A pre-1 minor release may change interfaces;
custom stores must follow the updated migration guide and conformance suite.
No intermediate v0.3 tag is required. Preserve every existing tag.

The release gate includes the full util Linux checks, green native Linux and
macOS scheduler checks and a visible native Windows result. Record an advisory
Windows failure as a support limit rather than calling the whole matrix green.
Publish through the repository release script from clean main, then verify the
public module fetch for the exact tag. Consumers update separately; this
release does not activate Cerberus, Torque or any other application's scheduler.

## Candidate stable surface

Before v1, enumerate and reconcile the actual exported surface in the tagged
source, rather than approving a list of planned symbols:

- `Schedule`, schedule-kind/time-zone/anchor/jitter and overlap/misfire policy
  descriptors, validation and next-occurrence helpers.
- Stable fire identity (`DeriveFireID`), durable fire/status/claim/transition
  contracts, pruning and non-recreation of retained occurrence identities.
- `Store`, `Runner`, `Job`, constructor/options, clock/ticker and start/stop/tick
  semantics, including configured worker and dispatch timeout behavior.
- Retry/backoff, claim recovery and stale-owner fences; explicit failure,
  exhaustion, duplicate dispatch and cancellation semantics.
- Passive observer events, metrics/slog projections and their concurrency,
  duration/lag definitions and durable-telemetry limitations.
- Reference `sqlstore` constructors/migration/connection options and the
  portable `conformance` contracts.

The freeze must preserve occurrence identity across restarts/retries and
at-least-once recovery of ambiguous enqueue outcomes. A successful Enqueue
is not proof of job completion. App-specific job types, permissions, activation
policy, execution, cancellation of already-enqueued jobs and run history remain
application-owned; they are not implied by a library v1 label.

## Evidence before a freeze decision

1. Verify custom-store migrations and conformance, including two engines over
   one file, stale CAS owners, expired claims and pruned-ID non-recreation.
2. Verify policy defaults and invalid-input refusal, DST gap/fold behavior,
   interval anchoring/jitter, bounded catch-up and queueing, and restart
   retention. Document every boundedness and execution guarantee precisely.
3. Verify lifecycle stop/drain, cancellation and timeout limitations with the
   actual exported API. A cancelled context cannot prove a runner stopped.
4. Obtain native Linux/macOS results and make an explicit Windows support
   decision from native tests. Advisory CI is not that decision.
5. Confirm exported interfaces are minimal enough for real embedding adapters,
   and record any unkeyed-struct-literal or required-Store-method migration.
6. Coordinate with maintainers of the other packages in `libs/util`: a v1 tag
   versions the entire module. A scheduler-only freeze cannot silently declare
   every other util package stable.

These gates are release planning, not authorization for live consumer adoption.
The designated module maintainer owns the freeze/version decision; embedding
app owners own their source migration and operational acceptance.

## Experimental surface and deprecations

Until those gates pass, the hardening APIs are pre-1 and may evolve in a minor
module release. Any future experimental API must say so in its own public
Go documentation and changelog; absence of that label does not manufacture
an early v1 guarantee.

At a ratified v1 freeze, document deprecated APIs with replacement and migration
examples before removing them. In particular, reconcile the existing deprecated
`Job.RunID` alias before the first v1 tag. After v1, preserve compatible behavior
within the major version; removals or incompatible contracts require a new
major module path/version. Exact deprecation windows are a maintainer decision
to record at freeze, not a date promised by this plan.
