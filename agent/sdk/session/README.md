# Experimental conversation coordination

`session.New(Ports)` composes authoritative host adapters. The SDK stores no
conversations and issues no identities. It reuses `mesh/core/runtimecontract`
bindings, environment receipts, references, recovery locators and committed
cursors. Provider-native session IDs belong in host adapters.

`Open` supports:

| Mode | Selection | Result |
| --- | --- | --- |
| Fresh | Explicit host-issued binding, generation 1, no parent | New conversation and first attempt |
| Continue | Exact parent binding and a distinct attempt at generation + 1 | Same owner, operative, seat and conversation; new runtime attempt |
| Relaunch / fork | Any request | `ErrUnsupported` before host calls |

The host authenticates access to every port. An owner label or a reference that
passes validation is not proof of authority. Concurrent separate conversations
for one operative are allowed. No method chooses a newest conversation.

Replacement reserves the successor as `Fenced` with host CAS, revokes the old
writer, checkpoints committed portable history, reconciles effects and old
approvals, saves recovery evidence, prepares an inert runtime, checks readiness
and current environment authority, then CASes the reservation to `Active`.
There is no automatic writer acquisition or queued-input replay. A host can
release explicitly queued input only after acquiring the new binding's writer
lease and checking it again at actual delivery.

Failures after reservation leave a fenced attempt. Read it with `View`, then
recover by explicitly selecting that failed binding as parent and supplying a
new attempt ID and the next generation. The old writer is never reactivated.
A failed or losing candidate is closed using its own instance handle, including
when the request context was cancelled. Activated runtime lifecycle and cleanup
of the fenced old runtime remain host responsibilities.

Multiple `View` callers observe one conversation without acquiring a lease.
`AcquireInput`, `RenewInput`, `RevokeInput` and `Submit` use the host's single
writer authority. `Admit` accepts bound event and approval delivery references.
Both delivery ports atomically recheck the full active binding; input also
checks the current lease and host expiry clock. Subsequent consumers must check
again before acting. Approval message admission never replaces gate decision,
operation digest, expiry, environment or effect-start verification.

`Recovery` reads an explicit durable pack. `LocateTranscript` and
`SearchTranscript` use owner/operative/conversation scope; adapters must enforce
that scope when resolving references and returning excerpts. `Pack` carries
transcript, plan, workspace, optional snapshot and pending-operation references,
environment lease, last committed turn and committed event cursor. Producer
sequence and PTY offsets are not committed cursors. Packs allow at most 256
operations and 256 approval receipts; hosts apply their own byte/retention bounds
and can retain a reference to a larger durable pending-operation ledger.

Reconciliation cannot discard operations, change their binding/digest, revert a
terminal outcome, turn uncertainty into `not_started`, or swap history/plan
references. A started effect must become a known outcome or `unknown`; old
pending approvals must become invalidated, terminal or unknown receipts. The
runtime receives uncertainty as evidence. This package has no replay executor.
Cross-provider recovery uses portable references for a fresh provider attempt;
it makes no claim about transferring hidden provider context or restoring files.

Host store mutations must resolve uncertain transport results before returning
a definitive commit/error result. Input and message IDs are idempotency keys:
unchanged content returns the original cursor; changed content conflicts.

The behavioral suite uses transactional in-memory fake hosts and inert fake
runtimes. It exercises concurrent writers/replacements, delayed old callbacks,
quota/crash recovery, readiness/CAS failure, cancelled preparation, cross-agent
history isolation and uncertain completed effects. It establishes this bounded
foundation only. Production storage and authentication adapters, runtime hosting,
JSONL/search adapters, explicit carry policies for relaunch/fork, application
integration and deployment remain consumer work.
