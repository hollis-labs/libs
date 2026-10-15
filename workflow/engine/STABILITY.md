# Stability and versioning

This module is pre-v1. Consumers must pin an exact immutable tag and should
review release notes plus the compatibility guards before upgrading.

## v0.1 compatibility line

Within `v0.1.x`, additive exported APIs and optional schema fields may be added
when old callers and persisted documents retain their meaning. The following
source-compatible names remain available throughout `v0.1.x`:

- `runtime/runtimetest` aliases;
- `runtime.WaitStatus`;
- `conformance.Host`; and
- `conformance.RunAll`.

Deprecation documentation may direct new code to replacements, but these names
will not be silently removed or repurposed in the compatibility line.

A breaking change requires at least `v0.2.0`. Breaking changes include removing
or changing an exported Go contract, schema identity, required field, enum or
error meaning; changing persisted state or event semantics; changing a digest
algorithm or its canonical input; changing migration or exact-version
resolution behavior; or changing an existing conformance fixture's accepted
meaning. A versioned migration or compatibility path and corresponding evidence
must accompany such a change.

## Stable identities

The extraction changed the Go module/import path only. Existing `HADR-*`
diagnostic codes, schema IDs, wire values, plan/graph digest algorithms, state
and event meanings, adapter identities, and fixture semantics remain unchanged.
Package moves inside the module, new root packages, or an unversioned fallback
to "latest" are not compatible changes.

The committed graph and execution-plan schemas, public API snapshot,
conformance fixture manifest, and digest snapshots are release gates. Updating
one requires an intentional compatibility review; regeneration alone is not
approval.

## Host responsibility

Module compatibility does not make a host durable. Hosts must pin exact plan,
step-kind, verifier, policy, schema, and adapter identities; migrate durable
records explicitly; and qualify their real adapters with the appropriate
conformance level. `runtime/inmemory` is a reference implementation, not a
production persistence promise.
