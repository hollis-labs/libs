# Gatecrasher coordinator

Experimental, security-sensitive coordinator for operation-bound human approval.
It implements `mesh/core/gate.GatePort` and `ReceiptSink`, using the existing
`mesh/core/hitl` lifecycle and `security/policy` evaluator. It runs no operation,
server, provider process, inbox or service lifecycle.

This candidate requires Go 1.26.11 and the **not yet published** `mesh/core`
v0.11.0 foundation. The dependency must be reviewed, published and resolved from
a clean consumer before landing this module. Local overlay results establish
source behavior only. No release or production integration is claimed.

## Host bindings

Construct `Config`, call `New`, then use `Request`, `Respond`, `Await`, `Cancel`,
`Deliver`, `RecordDelivery` and `RecordExecution`. All ports and their returned
values are trusted host dependencies; exposing these methods over a public API
requires authenticated callers, responders and receipt reporters.

| Binding | Responsibility |
|---|---|
| `PolicyPort` | Existing PDP/evaluator, explicit enforce/shadow and failure options, synchronous audit, and host resolution of verified facts and complete policy intent. |
| `ResponseAuthority` | Authenticate the responder from host context; authorize the exact option, scope, owner and current revision before HITL CAS; enforce self-approval and presence rules. Participant assertions/proof bytes alone never verify identity. |
| `gate.AuthorityVerifier` | Resolve the authoritative result and current host authority, policy, environment, resources and obligations. A caller-created decision or issuer reference is insufficient. |
| `InteractionPort` | One existing authenticated durable owner per request. Preserve its real item/revision and terminal CAS; never duplicate the decision into another owner. |
| `Journal` | Durable routing intent, immutable owner/scope/snapshot, mapping, delivery correlation, CAS versions, leases, attempts and separate effect reports. |
| `Origin` | Exact native request and live generation, operation digest recomputed from authoritative facts, options, launch mode, current grants and expiry. Fence these through the physical reply; reconcile old attempts separately. |

`Journal.Reserve` persists the request **and its owner/caller scope before** an
external enqueue. Stable-key enqueue can then be repeated after a crash without
a distributed transaction. `Recover` processes a bounded cursor page and attaches
actual owner handles, including a current terminal handle returned by idempotent
enqueue. It never sends or executes. Changing owner configuration cannot redirect
an unattached intent. The host must resume it through its pinned owner or perform
an explicit migration.

The reference interaction profile is `gate_native_v1`. The `gate_request` extra
field is a JSON **string containing the serialized gate request**. HITL recursively
canonicalizes JSON objects, so this string preserves opaque native payload bytes
and exact request identity. This is a new library profile, not a claim that an
existing application's approval card supports arbitrary native options. Production
presentation/owner adapters need lossless, authenticated profile integration.
Snapshots and proofs can contain sensitive data: retain them in scoped storage,
never routine audit messages.

## Policy and decision semantics

The coordinator supports exact `once` scopes with one use, and mandatory approval
obligations. It intersects those capabilities with the origin's advertised
support. Unsupported scope, operation, native decision or dry-run/rate obligation
is refused explicitly. It never turns a session option into once or a bool.

The policy binding checks request policy revision, ordered provenance and every
mandatory obligation against the complete evaluator intent. PDP errors, missing
audit or unsupported obligations issue no human decision. `ErrLocalPath` reports
an independently configured allow/shadow/audited failure path; it supplies **no
Gatecrasher approval and no execution authority**. The host must evaluate and
admit that path separately. Reconnecting a pending human interaction returns that
interaction; a later policy or coordinator outage never makes it allow.

`Respond` records the verified participant and exact option ID through the
owner's first-terminal-wins CAS. Decisions reload from that outcome, never from a
submitted response or coordinator decision map. Deny remains a selected decision;
explicit cancellation returns `cancelled`, expiry returns `expired`. A native
cancel option, when advertised, remains an exact selected native choice. Display
labels do not identify options. Native JSON IDs retain string versus number and
integers above float64 precision.

`Await` timeout/context cancellation only ends the wait. `Cancel` currently
supports `hitl.CauseCallerWithdrawn`; other causes refuse rather than silently
changing their meaning. Absolute expiry races with response/cancel at HITL,
without relying on a sweeper. Changed arguments, plan, resource version,
generation, native options or scope conflict under the same idempotency key.
They require fresh host evaluation/request identity.

## Delivery and execution recovery

`Deliver` reloads the authoritative outcome. Before a **new allow reply**, it
verifies authority and `gate.ValidateAdmission`; the origin independently checks
and fences current host authority, digest, generation and expiry through its
physical write. A provider can unblock an effect as soon as it receives allow.
Earlier coordinator preflight is insufficient if authority or the runtime changes
before writing. Refuse an adapter that cannot provide the required boundary.

The journal records an unknown attempt before sending. A lost receipt, abandoned
lease or transport-only `sent` status requires reconciliation. Only an
authoritative negative result with evidence, accounting for earlier in-flight
writes through fencing/idempotency, permits a new same-request send. Unknown
stays unknown. Historical acknowledgement can be read/reconciled after expiry or
runtime replacement; that observation never grants a new send to the replacement.
Lease expiry alone is not proof an earlier sender stopped.

Physical replies may be repeated after a crash. The exact recipient must dedupe,
fence or reconcile them. There is **no exactly-once network delivery or external
effect guarantee**. A transport write is not acknowledgement, acknowledgement is
not execution success, and uncertain execution never authorizes replay.

The originating executor recomputes the versioned operation descriptor and
atomically consumes once scope at local admission, fenced through effect start.
The coordinator neither claims that grant nor executes the operation. Execution
receipts are separate observations. After an unknown effect report, a changed
status requires host reconciliation evidence; terminal effect reports cannot be
overwritten. Receipt reporting starts after `Deliver` establishes the decision
correlation. Reporters must be authenticated by the host.

## Behavioral evidence and limits

Run `go test -race ./...` or `go test -run ExampleCoordinator`. The example uses
the real HITL service with deterministic fake policy/identity/origin/journal
ports, then performs an explicit fake executor admission and reports its effect.
The restartable journal serializes its image and reconstructs a new adapter; it
is test-only and proves no production filesystem/database durability guarantees.

Fixtures cover competing responses, cancel/expiry races, reconnect/conflicting
snapshots, proof/issuer spoofing, owner/revision mismatch, unsupported scopes and
obligations, request intent/mapping crash windows, decision-before-send,
send-before-journal/ack, positive/negative/unknown reconciliation, historical ack
after expiry/replacement, blocked senders after lease reclaim, authority revocation
before write and unknown effects with atomic fake once consumption.

Provider-shaped command-escalation, pinned MCP-tool-approval host projections,
ACP exact option/turn-cancel and launch-mode negative controls exercise coordinator
semantics. They do not test official provider schema conformance, callback
reachability or live enforcement. Provider async seams, real SDK adoption,
production durable journal/owner adapters, existing UI profiles and thin host
remain separate integrations. There are no SDK, harness or application imports,
posture changes, deployment or live activation in this slice.
