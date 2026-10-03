# Contribution registry protocol 2

This is the shared implementation contract for CW-20261003-0037, based on
CW-20261003-0025 and CW-20261003-0026. Protocol 1 is rejected; there is no fallback
or compatibility shim. Nanite remains pinned to its released SDK during wave 1.

## Wire fields

The response requires `protocol: 2`, `host_instance` (opaque host epoch),
`revision` (positive integer, at most 2^53−1), `plugins`, `kinds`, `regions`,
`contributions`, and `refusals`. Empty maps are `{}` and empty lists are `[]`.
Duplicate JSON object keys are collisions, including nested keys.

`plugins[owner_id]` contains `owner_generation` and optional `bundle_url`,
`bundle_version`, `stylesheet_url`, and `runtime`. Bundle URL and version appear
together. Version is exactly `sha256:` followed by 64 lowercase hexadecimal
characters and identifies the executable bundle bytes. Runtime is an array of
`{name, min?, max?}` entries: inclusive normalized semantic-version bounds,
at least one bound, no duplicate names. Hosts provide actual named versions;
missing, malformed, or incompatible versions fail admission. Prerelease host
versions require explicit opt-in. Integrity must be checked before execution of
those same bytes; fetching an unchecked URL again is insufficient.

`kinds[name]` contains `schema_version`, `metadata_schema`, `representations`,
`regions`, and `required_capabilities`. `regions[name]` contains `kinds`,
`representations`, `context_schema`, and `ordering` (`priority-ascending`,
`priority-descending`, or `manifest`). Names are open strings; there is no closed
built-in kind enumeration. Descriptors declare contracts, never grant authority.

`contributions[kind][owner_id + "/" + local_key]` contains `owner_id`,
`owner_generation`, `local_key`, `kind`, `schema_version`, explicit boolean
`required`, `representation`, and JSON `metadata`. Optional `public_binding`
is unique within a kind. Owner IDs and local keys match `[A-Za-z0-9_.-]+`.
Generation is opaque and must match its plugin record. Exactly one
representation-specific field is allowed:

- `component: {export, region}` requires a bundle and a supported component region.
- `declarative: <JSON value>` is data, with no executable module required.
- `handler: {id}` names a public reviewed binding, never a private execution token.

`refusals[]` contains `owner_id`, `owner_generation`, `kind`, `local_key`, `reason`,
and `required`. An unknown optional kind remains structurally valid, then receives
a named admission refusal. Any required refusal aborts the candidate's activation.
Optional refusals preserve admitted siblings.

## Host admission

The host explicitly opts into kinds and component regions. Both published and
local descriptors must admit the schema version, representation, and region.
The host supplies its schema validator; a nonempty metadata schema cannot be
silently accepted without one. Reserved owner `core`, host-reserved names, and
another owner's `plugin.<owner>.*` namespace are refused.

Admission refusal codes are `reserved`, `unsupported-kind`, `unsupported-schema`,
`unsupported-representation`, `unsupported-region`, `invalid-metadata`, and
`unsupported-metadata-schema`. Structural failures are separate errors:
`ErrProtocol`, `ErrInvalidContribution`, `ErrUnknownPlugin`, `ErrCollision`,
`ErrIntegrity`, and `ErrRuntime`; required admission refusal is `ErrRequired`.
Lifecycle errors are `ErrStale`, `ErrNeedsRevocation`, and `ErrRevoked`.
The registry does not grant capabilities or enforce server-side authorization;
those remain host obligations. Context schemas are consumed by host adapters.

## Revision and disposal contract

Snapshots are complete immutable catalog revisions within one host epoch.
Publication is atomic; runtime replacement is deliberately not an atomic swap.
A different generation or bundle identity cannot replace a current owner until
that owner is explicitly revoked. A revoked generation cannot be reactivated.

1. Validate and preflight the candidate, including runtime and exact bundle bytes,
   while the existing generation may still serve. Failure keeps the existing one.
2. Revoke the old generation: fence new calls, cancel admitted work, and publish
   its absence as a new catalog revision.
3. Dispose owned resources in reverse registration order, attempting every
   disposer and retaining a combined failure report. Disposal is idempotent.
4. Only after successful disposal may the host load/adopt the new generation.
5. Publish its complete admitted contribution set in a later revision. Failed
   post-revocation load stays unavailable; do not silently restore the old one.

There is no callable overlap. Host adapters must test epoch and generation before
using captured exports, action bindings, subscriptions, or async completions.
`Catalog` owns snapshot publication and generation tombstones, not plugin
processes. `Scope` fences admission and provides cancellation and reverse cleanup;
host code owns the orchestration between them. Callbacks must cooperate with
cancellation: an in-process library cannot forcibly stop arbitrary code.

## Shared fixtures and split ownership

`registry/testdata/contract/protocol-2/*.json` carries `description`, `response`,
`go.validate`, and `ts.validate`. Validation expectations describe structural
validation, not host-specific admission. The unknown-optional fixture is `ok`
structurally and must separately exercise `unsupported-kind` admission.
Protocol-1 fixtures remain frozen historical evidence, not supported input.

The first PR owns Go, these fixtures, and this summary. The stacked second PR owns
TypeScript types, verified-byte loader, React adapter, and TypeScript conformance.
Any subsequent fixture change must be communicated to both implementation owners.
No release, tag, application adoption, MCP transport, or private handler credentials
are included in these PRs.
