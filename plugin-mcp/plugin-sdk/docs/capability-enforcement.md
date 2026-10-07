# Host call enforcement

`capability/host` supplies planning and per-call helpers around the shared
`capability.Grant`, `GrantSet` and `RuntimeIdentity` DTOs. It authenticates no
application transport and imports no subprocess transport or application code.
Use a host-owned adapter on every RPC, HTTP, MCP and UI operation surface.
An Init grant is discovery data; it is not an authenticated request.

## Planning

Call `ResolveGrants` before spawning or registering a plugin. Supply normalized
`Request` scopes, a host-selected descriptor catalog, a `ScopeResolver` for
supported/operator-approved/current-policy scopes, and a `GrantPlan` with the
lifecycle-issued runtime identity, audience, issue/expiry timestamps, policy
revision and fresh grant IDs. Requests are separate from the sole Init wire DTO.
The result is the shared `GrantSet` ready for that DTO; no parallel wire grant
is created here.

Every scope is validated against the selected descriptor/version and authority
is intersected without widening. Optional refusal produces a named
`PlanNotice` with a code;
required refusal aborts the complete plan rather than returning partial grants.
Cancellation or implementation failure aborts even an optional request. Empty
plans encode as `[]`. An admitted scope that narrows a request also returns a
`PlanNotice{Narrowed:true}` identifying its grant, including required requests.
A broken scope callback or grant ID generator aborts the plan. Repeated
capability names with different grant IDs retain
separate operation/target/key pairs. Hosts still review digest/runtime/OS
permissions and use their own lifecycle controller before spawn/activation.

## Call-time adapter

Construct an `Enforcer` through `NewEnforcer` with a fixed expected host epoch
and audience, the selected catalog, a trusted `AuthorityResolver`,
an atomic `Budget`, an optional `Auditor` and an injectable clock. Configure
callbacks before use; do not mutate configuration concurrently. The resolver
must authenticate the actual connection/client, verify its narrow binding,
resolve the current grant, initiating caller and current policy, and supply the
same lifecycle-issued tuple in a consistent, immutable snapshot. Resolver and
budget adapters must be safe for concurrent calls. Never derive facts from caller-selected
headers, plugin session/agent IDs or Init's connection-level identity. A plugin
actor ID must match its owner. Each authority must select either a verified
initiating caller with a separate caller policy, or explicit `Background:true`
for a plugin actor with only approved background policy scope. Non-plugin
actors require a verified initiating caller and its policy. Missing mode, both
modes or a caller policy without a caller fails closed. Verified callers may be users
(`UserActor`), agents, sessions or MCP proxies. User callers do not receive
bearer credentials from this library.

`Call` contains host-normalized demands, not raw arguments. Supply capability,
grant, exact operation/target/effect, request/trace IDs, every descriptor-specific
identifier dimension, and measured/reserved usage for every numeric limit.
`Call.RequestID` uses the leaf positive safe-integer type. Audit records its
decimal representation. An invalid ID produces an internal invalid_request
classification without RPC correlation; the adapter must emit a standard
JSON-RPC envelope error rather than invent an application error ID.
A missing dimension/usage fails closed. Core operation/target/effect cannot be
overridden through the dimensions map. Canonical paired identifiers must include
all reviewed identity/revision information; the helper does not resolve URLs,
secrets, tools or application targets, classify effect hints, validate RPC
payloads or count actual response bytes.

`Authority` must include an authenticated actor, expected tuple/audience,
structurally valid live grant, active/available target, exact current policy
revision, policy scopes and a lifecycle/grant/binding cancellation context.
An epoch or generation mismatch returns target_unavailable/stale_binding.
A grant or permit belonging to a different owner returns capability_denied.
The selected descriptor owns the operation; two catalog descriptors cannot
share an operation name. Scope envelopes use exact key casing and reject
case variants or duplicate keys before interpreting authority.
Every actor needs a `TransportScope` that can only narrow the grant. For a
plugin it comes from its authenticated connection binding; for a non-plugin
route it comes from the verified scoped credential. Non-plugin routes verify
that credential separately and resolve its referenced live grant/current policy
here. Omitting transport scope denies rather than inheriting the full grant.
The helper checks identity, tuple, audience, issue/expiry, descriptor/schema,
current policy,
caller intersection, binding, target, operation, effect and all budgets before
calling any effect handler. Only explicit provisional `log.write`/`host/log`
authority may operate while inactive; it still needs a live bounded lease and
all normal grant checks. It creates no other pre-activation service lane.

`Budget.Reserve` must atomically reserve cumulative usage/rate/concurrency across
requests, observe cancellation and return a nonnil release function. It must
not perform the application effect. A callback that panics before returning a
release handle must roll back its own partial reservation; the library cannot
release a handle it never received. Resolver and budget callbacks must honor
the context and return promptly; an uncooperative resolver can block its caller.
Resolver, reservation, commit, cleanup and policy callback panics are contained
as safe failures. Actual operation implementations enforce reserved
row/body/output limits, current definition/digest, cycles and target
ownership. Missing reservation adapters deny even byte-only operations.

Prefer `Run(ctx, call, handler)`: it checks/reserves, rechecks after reservation,
invokes the handler, releases reservations and audits the outcome on success,
error or panic. The handler receives a `Permit` and cancellation context. If it
waits before mutation, call `Permit.Recheck` after the wait under the host's
lifecycle admission/commit guard before writing. `Run` consumes one commit
attempt around its handler and does not retry it. That guard is the atomic
boundary: a cancellation context alone cannot prevent a revoke/commit race.
Current policy and identity are resolved again; a changed actor, caller,
audience, tuple or policy revision cannot borrow the admitted permit.

`RequireCapability` is the lower-level helper for adapters needing their own
execution wrapper. It audits admission, returning a reserved `Permit`. After
any wait, call `Permit.Commit` under the host commit guard; it rechecks, consumes
one attempt and invokes the effect. A second commit attempt returns conflict,
even when the first failed. Defer `Close` on every path and audit the application
outcome through host instrumentation. Cleanup callbacks are attempted once;
their panics increment `CleanupFailures` and cannot rewrite a definite commit.
Closing during a commit cancels it but retains its reservation until the
callback actually finishes. A failed cleanup callback is reported rather than
claimed to have released the host's resources. A permit's admission audit has
effect state not_started;
`Run` reports a definite completed operation as committed. Do not hold permits
without bounded lifetimes or copy an Enforcer after use.

Grant expiry, request cancellation and authority withdrawal cancel the permit.
A scope's per-call deadline further shortens its lifetime and cannot be renewed
or extended by rechecking. Recheck also consults the injected clock so a delayed
timer cannot permit a late effect. A definite successful effect stays successful
even if cancellation arrives after commit. An untyped failure/panic after a
possibly mutating handler starts becomes unknown_outcome; typed failures retain
validated effect states. No automatic retry or fictional rollback occurs.

## Audit and host conformance

Outcomes include authenticated actor/initiating caller, tuple, grant/policy,
request/trace, target/tool/effect and duration, with no arguments, content,
credentials or raw error messages. Every audited identifier is limited to
256 UTF-8 bytes; invalid, control, Unicode format and bidi-bearing values become
the reserved wildcard marker `*`, which cannot be a valid identifier. A real
identifier named `[invalid]` is preserved and remains distinguishable.
Unauthenticated actor/caller assertions are scrubbed. Audit receives a context
without request cancellation so a cancelled request cannot erase its outcome;
the sink must apply its own bounded timeout. Sink errors/panics do not change
enforcement;
`Enforcer.Counters` keeps helper success/failure and audit failure counts readable
independently of telemetry presentation. Counters describe helper events, not
all operations in an application. Sinks must remain bounded and return promptly.

The package tests use synthetic authenticated authority/policy/budget adapters
and observable effects. They prove helper behavior, not a real host transport,
OS boundary or registry. Behavioral host conformance must exercise the actual
adapter and demonstrate that deliberately broken adapters fail; that suite is
a separate delivery. A host declares unsupported descriptors through its catalog
subset. No consumer adoption, protocol Serve change, HTTP bridge, workflow
engine or OS sandbox is implemented by these helpers.
