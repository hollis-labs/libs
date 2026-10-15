// Package teams provides portable team configuration, a pure flex-phase
// compiler, authority checks, snapshot addressing and journaled launch repair.
// Host interfaces supply execution,
// persistence, identity authentication, workflow waits and message transport.
//
// A host stores immutable Team revisions and authenticates actor URNs before
// passing them to policy operations. In development mode only a slot/actor
// with no grants is open; any grant makes missing verbs fail closed. Approval
// always needs an explicit grant and never permits an author to review their
// own work. Phase inputs select authored policy only by ID; supplied fields
// never override team policy. Roster inputs to Signal, Advance and approval
// checks must be trusted host snapshots, not caller-authored wire data.
// Authors may be active or ended run members; unknown authors fail
// closed. Roster administration requires an owner/admin governance role or
// an explicit administrative grant, even in development mode.
//
// CompileTeam validates a Team and compiles exactly one flex phase.
// Gate execution is unsupported.
// Signal records a permitted member's true trigger. Advance resolves a phase
// durably once, guarded by owner/authorized-slot policy (or Auto). The host
// advances its ordinary workflow and acknowledges waits and stand-down.
//
// Launcher is an initial host-authorized formation operation. LaunchLedger
// leases must serialize and fence operations across all host processes.
// Host adapters persist immutable keyed intents before external side effects,
// recover existing resources on replay and reject changed requests. Reconcile
// takes a caller-owned rotating cursor; there is no background scheduler.
// LaunchRequest.PoolIdentities selects a run-scoped subset of the enrolled pool
// for formation and later spawning; the full authored pool remains reserved.
// Definitions require pinned definition references and distinct enrolled identity
// references for pool slots. Concurrent activation requires a pool. Hosts verify
// enrollment and definition pins; syntactic validation cannot query a registry.
// Fresh members receive enrolled ephemeral identities with no persistent home or
// mailbox continuity. Retirement fences provisioning and ends their enrollment.
// Caller cancellation and context timeouts are retryable interruptions.
// Initial launch attempts are bounded by retry count and timeout. Failure is
// journaled before workflow fencing and full-roster non-routable reservation.
// Cleanup terminates descendants before roots and fences every planned intent, including
// lost acknowledgements. Release/Retire touch only resources acquired by the
// intent key; never-acknowledged stubs carry no actor identity. Fresh results may
// not reuse a declared stable identity or any prior roster identity.
// Reconcile resumes interrupted cleanup to terminal Failed;
// unavailable adapters can retain Aborting records requiring host repair.
// A LaunchLedger lease serializes one launch key. It is distinct from a host
// binding lease for an actor, a controller epoch or a workspace pin lock.
//
// Dynamic Spawn inherits parent ceilings and requests may only tighten them.
// MaxDepth is an absolute distance from the run root; budget is allocated
// within the parent's remaining budget. Quota reservations are committed
// before provisioning. Transient errors retain quota; ErrProvisionFailed
// cleans and fences the intent before marking it failed and freeing quota.
// ReconcileMembers repairs provisioning and termination acknowledgements.
// Failed descendants block their ancestors while unrelated recovery continues.
// Every termination is reserved as non-routable before external host calls.
// Provisioner Retire/Release must fence racing keyed provisions; the library
// also compensates a live result whose roster commit loses to cancellation.
// Pool provisioning leases registered stable identities, one session each.
// The memory host is a test fake, with explicit spawn capability assignment;
// it does not implement real pool allocation or durable storage.
//
// Router sends against one retained roster version and reports provenance
// and partial failures. SendResolved loads that immutable version to verify
// sender and recipients and rechecks authority. An already authorized send
// may finish after later member removal. BindMessage ties each logical send
// key to its content and roster plan before any transport delivery. Hosts
// retain the Route and original request for retries and call SendResolved.
// Calling Send again resolves a new route; dispatch rotation can produce a
// different plan and thus a conflict for an already-bound key. SnapshotAt
// retains referenced versions until pending sends finish or are abandoned;
// unreferenced versions may be evicted, and missing versions fail closed. Lazy
// resolution uses Spawn directly; host calls run without router/roster locks.
// Dispatch rotation remains local to a Router instance. Broadcast addresses
// include the sender, work dispatch may pick the sender, and history policy
// defaults to summary. Per-call history may narrow the team policy in order
// full > filtered > summary > none. ReplyResult uses DeliveryStore accepted
// delegations and current state to queue replies for the original next turn;
// new results require both original member/session pairs to remain active.
// Accepted result replay recovers lost acknowledgements without redelivery.
// EndRun releases stable identities, retires fresh
// identities and commits terminal membership; the host owns workflow state.
package teams
