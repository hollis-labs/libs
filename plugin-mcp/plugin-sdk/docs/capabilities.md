# Host capability primitives

The `capability` package defines a shared vocabulary, exact-match scope algebra,
versioned descriptors and safe application errors. It uses only the standard
library. Importing it or discovering a descriptor grants no authority.

Hosts select supported shared names with `NewCatalog`. The seed names are
`readonly.query`, `context.source`, `durable_agent.wake`, `reflex.seed` and
`mcp.reach`. Reverse-service proposals (`storage.read`, `storage.write`,
`secrets.read`, `egress.request`, `events.publish`, `log.write`) have
`Proposed: true`; their presence does not ratify a service or install a handler.
Hosts publish the selected descriptors and implement their chosen operations.

Extensions use `host.<canonical-id>.<operation>` or
`plugin.<canonical-id>.<operation>`. A shared name cannot be replaced even when
it is absent from the host's supported subset. Catalog inputs and lookup results
are copied so caller mutation cannot replace registered semantics.

## Normalized scopes

`Scope` is an enforcement envelope, separate from the wire Grant DTO. It has
exact identifier `allowlists` and nonnegative numeric `limits`. Descriptors
publish a closed `ScopeSchema` listing accepted dimensions and ceilings; an
unknown schema version, unknown field, missing field, unsupported operation or
increased effect fails by capability name. Every dimension is explicit, even
when denied (empty list or zero limit). There are no wildcard semantics.

Operations are exact names; reverse-service operations are the fixed
`host/...` RPC methods. Effects are `read`, `write` or `destructive`, checked
against the descriptor ceiling. A ceiling is a maximum, not permission: the
scope must explicitly include the actual effect. Hosts classify actual effects,
including egress and delete, independently of caller hints.

Identifiers are host-canonical and exact, including resources, targets,
sessions, agents, sources, mounts, keys, secret references, event/schema pairs,
HTTPS destinations/methods and visibility levels. Related identifiers such as
server/tool or definition/revision pairs must be encoded as one unambiguous
host-canonical identifier in the corresponding dimension. Splitting paired
permissions into separate allowlists would accidentally authorize their
Cartesian product. The host must pin reviewed definitions and verify their
current revision. This library does not resolve identifiers or URLs.

Limit units are bytes (`request_bytes`, `response_bytes`), rows (`rows`),
milliseconds (`deadline_ms`, `lifetime_ms`), simultaneous calls (`concurrency`),
calls per minute (`rate_per_minute`) and delegation depth (`call_depth`).
Limits are ceilings; the host must measure, reserve and enforce them at the
operation boundary. Schema validation alone does not enforce cumulative usage.

Compute effective authority with
`Intersect(name, requested, supported, approved, policy)`. Missing constraints
convey no authority. Each allowlist is intersected and each ceiling takes the
minimum, with independently owned output maps and slices. Check every candidate
renewal/narrowing with `CheckNarrowing(name, previous, next)`; new identifiers or
larger ceilings return `scope_denied` naming the capability. Validate all scopes
against the same selected descriptor version before intersecting.

```go
catalog, err := capability.NewCatalog([]string{capability.StorageRead}, nil)
// Handle err before using catalog.
descriptor, err := catalog.Lookup(capability.StorageRead, 1)
// Validate the requested/supported/approved/current-policy scopes separately.
err = descriptor.ValidateScope(1, requested)
effective, err := capability.Intersect(descriptor.Name, requested,
    supported, approved, currentPolicy)
// Handle each error. These steps resolve scope; they do not authenticate a call.
```

## Typed failures

`Error` carries a symbolic `Code`, capability name, request ID and effect state,
without an internal cause or arbitrary diagnostic text. `RPCData` returns the
`host-rpc/1` payload for application failures at JSON-RPC code `-32010`.
Every payload has `retryable: false`. Standard JSON-RPC parsing/invalid-method
failures remain distinct. Effect states are `not_started`, `not_committed`,
`committed` and `unknown`; an ambiguous submitted mutation uses
`unknown_outcome`, never an automatically retried timeout or claimed rollback.
Adapters must not copy arguments, secrets or raw internal errors into failures.

## Integration boundary

These primitives do not authenticate subjects, issue grants or credentials,
check current owner generations, authorize a transport handler, implement an
RPC service or enforce OS sandboxing. They establish the descriptor/scope/error
foundation for those host helpers. Hosts must still enforce current identity,
canonical owner tuple, audience, expiry, caller policy, target, operation,
effect and budgets before any side effect and again at a delayed commit boundary.
Plugin authority uses the authenticated stdio connection and narrow-only
bindings; bearer credentials are reserved for verified non-plugin clients.
