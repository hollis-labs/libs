# Operation-bound gate contract

Experimental neutral approval ports for SDK-native tools and provider adapters.
Import `github.com/hollis-labs/libs/mesh/core/gate`; wire contract `1` is independent
of the module version. This package does not implement a coordinator, PDP, inbox,
provider hook, identity issuer or effect executor.

`GatePort` negotiates capabilities for an exact adapter/protocol/launch mode,
accepts immutable requests, waits and explicitly cancels. `ReceiptSink` separately
records delivery and execution observations. `gate/gatetest` provides synthetic
shared inputs; its references never carry real authority.

A request binds an enforcement point, operation ID and host-computed operation
digest to the complete operative/conversation/owner/attempt/generation binding.
Native JSON IDs retain string versus number and numeric precision. Options retain
the exact native ID, semantic decision, scope, target, use limit, expiry and opaque
payload. The host filters offered choices against policy; provider advertising is
not authority. There is no ordering that silently upgrades once to session or
policy amendment. Unsupported options, launch modes, operation classes and
mandatory obligations refuse through `Capabilities.ValidateRequest`.

`Request.Snapshot` detaches payloads, slices and pointers before acceptance.
`Request.Digest` identifies that immutable gate snapshot, including policy revision,
ordered provenance, choices and environment receipt. It compacts opaque JSON
whitespace but retains raw payload object-key order and numeric spelling: equivalent
reordered payloads can conservatively conflict. It is not a general canonical JSON
algorithm. Reusing an idempotency key with a changed snapshot must conflict.

The operation digest is different. Its scheme is `sha256`, value is 64 lowercase
hex characters and `DescriptorVersion` identifies the host's canonical descriptor.
The host must hash authoritative action, arguments, targets, plan/resource versions,
workspace/environment identity and effects. It must explicitly refuse a binding
when those facts or a supported descriptor version are unavailable. A summary,
policy revision or HITL idempotency digest cannot substitute. Recompute the digest
from live facts before execution; changed facts require a new evaluation/request.

A ticket names its HITL authority, item, request snapshot digest and binding. There
is one authoritative HITL terminal outcome, selected with first-terminal-wins CAS.
Responder authentication and authorization precede that CAS. A `Decision` is its
projection, including the exact chosen option and a `hitl.ResolutionRecord`.
Participant, proof and authority-reference fields are assertions until a trusted
host resolves and verifies them. A replayed ticket may contain HITL's current
terminal revision; equal decision/handle revisions remain valid after verification.

`ValidateAdmission` checks shape, exact snapshot/binding/adapter/enforcement point,
operation digest, selected option/scope and absolute deadlines. It then invokes the
injected `AuthorityVerifier`. The verifier must resolve the actual authoritative
outcome, authenticate and authorize the responder (including required presence and
no-self-approval), and check current host facts, environment/lease, grants, policy
and mandatory obligations. Constructing a shape-valid environment receipt is not
verification. Any verifier error refuses admission.

After validation, the executor must atomically claim once/limited grants and fence
live binding/lease changes through effect start. Validation alone does not close
that race. A cancelled context never authorizes a claim. A failed/uncertain attempt
must retain its claim until reconciled, rather than freeing it for automatic replay.
No exactly-once external effect guarantee is made here.

Await timeout/cancellation ends only the wait. Explicit Cancel and expiry compete
with responses in the HITL authority. Pending, denied, expired and cancelled
requests authorize no effects. A coordinator outage never converts pending to allow.

Delivery pending/sent/acknowledged/rejected/unknown and execution
not_started/started/succeeded/failed/cancelled/unknown are separate observations.
Sent does not mean acknowledged; acknowledged does not mean an effect succeeded.
Retry only an exact live native request when the host has established retry safety.
Unknown acknowledgement or effect requires reconciliation. Never route a generation
7 response to generation 8, even within the same conversation.

The behavioral fixtures use the actual HITL Service and memory Store, plus fake
host authorization and atomic admission. Both SDK-tool and native-adapter consumers
exercise once, refusal, uncertainty and identity fencing with the same requests.
Additional cases cover racing responses/cancellation, snapshot mutation, terminal
replay, exact native IDs/options and unknown mandatory obligations. These are local
contract proofs; they do not establish production durability, provider callback
reachability, bounded-grant accounting, deployment or live authority integration.
