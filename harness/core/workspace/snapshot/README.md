# Filesystem snapshots

Import `github.com/hollis-labs/substrate/harness/workspace/snapshot`.
This leaf preserves the six historical `go-agent-wrapper/snapshot` source,
fixture and benchmark files with their history. It has no workspace-root or
application dependency. Existing `harness/sandbox/snapshot` callers remain
compatible; new host integration uses this package.

`workspace.Ports.Snapshots` accepts `FilesystemSnapshotProvider`, whose methods
are `Capture`, `Diff`, `Preview` and selective `Restore`. Nil means disabled.
Workspace materialization never calls it automatically. Supplying a provider
is a mechanism choice, not authority or proof of required capture coverage.

Before capture, a host must supply an explicit policy with validated finite
capture, storage and per-run work budgets, resolved effective grants, mandatory
secret exclusions and verified confidential store custody. Without that policy,
capture stays disabled. Required unavailable coverage must refuse; the historical
`NoOpProvider` returns empty success and cannot prove coverage. ShadowGit creates
owned fixture stores, not protected host custody merely by using mode 0700.

Hosts own stable target/root/store binding, snapshot-set IDs, per-root failure
accounting, journal correlation and coalesced reattach intervals. Capture is
best effort per target and is neither cross-root atomic nor cross-process locked.
Ignored, excluded and oversized files limit coverage. Snapshots contain private
file content; neither hashes nor Git isolation prove secret exclusion.

The original `Restore` warns and overwrites differing content. It does not
implement expected-current-hash CAS or host authority revalidation. Empty paths
restore nothing. The guarded `RestoreSelective` path below requires expected-current presence
and hash plus a continuously held host writer fence. A captured-tree fork into
a new owned root remains a separate integration.

Default retention is KEEP. Pins for journal replay, exports and forks dominate
all GC limits; exhausted pinned budgets stop or defer captures. The low-level
`Cleaner.Cleanup` and `ShadowGit.Purge` have no pin, custody or authority proof.
Do not expose or invoke them as host GC until those prerequisites are established.
No automatic retention duration, COW capability, runtime adoption or live capture
is supplied by this package move.

## Guarded host retention

`CapturePolicy.Retention.Roots` optionally overrides age/count selection for a
stable root ID. An explicit zero override means KEEP; omitted roots inherit the
global rule. Root counts rank sets containing that root. A multi-root set is
eligible only when every root permits collection, and outstanding journal,
export and fork pins always dominate these rules.

`GuardedProvider.Collect` accepts an operation-bound opaque `GCGrant`; policy,
JSON, elapsed age and decoded completion data cannot issue that authority. This
release has no production host, deletion or owner-completion issuer. Protected
construction requires independently observed OS exclusion and a genuine host
admission port; missing or stale support refuses. The private GC kernel is
verified in disposable zero-agent fixtures; it is not live GC.

`ReadLease.CompleteFromHost` consumes a privately issued
`SnapshotCompletionProof` from the already-held `SnapshotCompletionAdmission`.
The typed request binds the exact physical operation, ledger store, set digest,
pin owner/kind and completion operation. The host verifies durable completion
and all owner obligations; decoded terminal data cannot release a pin.
`GuardedProvider.CollectFromHost` similarly consumes `SnapshotGCProof` through
`SnapshotGCAdmission`, with separate current deletion and noncooperating-reader
authority. It reuses the guarded collector rather than exposing a grant factory.
These are consumption contracts; no production proof issuer is supplied.

Completion retains its pin until host recording and successful host closure.
Both paths persist an uncertainty barrier before accounting/effects and hold
the native store lock through host verification, recording and closure. Failed
or stale proof, persistence failure and uncertain closure keep the barrier and
block later capture/read/collection. Final completion bookkeeping happens only
after successful host closure, under that same native lock; it authorizes no
further source or object effects. Retrying an uncertain operation refuses.

The kernel holds the store admission lock, verifies the complete owned Git
reference/object map and records intent before mutation. It CAS-deletes only
exact capture-created references and reclaims unreachable private objects.
Pinned captures and shared reachable objects survive. Captures from before
exact reference accounting, unknown references/objects and uncertain captures
refuse collection. `GCPurge` also obeys KEEP and pins; it never force-deletes a
store directory or invokes legacy `ShadowGit.Purge`.

Completed operations are durably idempotent. Reusing an operation ID with
changed input refuses; a recorded no-op remains a historical no-op even when
later pin state changes. Tombstones preserve consumed run budgets. Reclaimed
bytes describe measured file-size change, not allocated disk blocks or an OS
quota. Interrupted effects retain a journal and block later capture/read/GC
admission. No crash recovery or automatic destructive retry is provided.

The root `workspace.CollectSnapshots` and `workspace.RunSnapshotGC` helpers
support explicit on-demand and cancellable finite schedules. Hosts supply
current issued authority and record redacted outcomes. Scheduling requires a
positive interval, per-attempt timeout and finite attempt count, and stops on
the first refusal or uncertain result. These helpers install no app scheduler,
shim policy or live cleanup authority. Legacy raw cleanup APIs remain separate.

## Observed isolation and admitted capture

`ObserveIsolation` observes an already-frozen provider-only Linux cgroup v2 and
its canonical payload. It does not launch, freeze, signal or thaw a process.
The request includes the exact payload PID/start time, opened cgroup directory,
immutable target plan, private store and complete canonical protected control,
journal and lock directory inventory. The host must independently reconcile
that inventory with its current execution; a supplied path or digest is not
authority. The sealed proof rechecks namespaces, thread capabilities, mounts,
private proc, inherited descriptors, protected file mappings and physical root
identities. Unsupported evidence, missing control inventory, thaw, process loss
or changed custody refuses. Other platforms have no supported observer yet.

`NewGuardedProvider` additionally requires a genuine `SnapshotHost`, an explicit
finite policy and a version-bound `ContentRedactor`. Construction is effectless;
first capture acquires host admission before creating ledger or Git state.
The host must independently verify current principal, policy, binding,
incarnation/controller and the exact operation, and continuously hold every
submission/source-writer and in-flight-I/O boundary. A callback returning nil,
policy fields or the observation alone supplies no host authority.

Eligible source bytes pass the existing credential exclusions and bounded
redaction adapter before any mirror write or Git ingestion. Redaction adapts
host-resolved values without credential discovery; it promises no universal PII
or arbitrary-secret detection. Read pins also require current physical and host
admission. An uncertain effect retains its evidence and pin, and host closure
must keep writer admission fenced rather than thaw on cancellation.

## Selective restore

`RestoreSelective` accepts a bound issued retained set and explicit
`RestoreSelection` values naming target, relative path and expected current
presence/SHA256. It refuses empty/duplicate/out-of-scope selections, symlinks,
credential hard-link aliases, stale roots and conflicts. All selections are
checked before staging; hashing, per-file replacement and durable accounting
share the same actual host writer/custody fence. Advisory flock and a final hash
comparison cannot supply that fence.

The Linux kernel records intent before atomic no-replace creation or exchange.
Exchange retains original content under its recorded staging name in the selected root;
there is no automatic deletion or compensation. Multi-file restore is not an
atomic transaction. `RestoreResult` preserves changed count, partial/uncertain
outcome and inspection/pin/fence obligations after late refusal or persistence
failure. Reusing a recorded operation refuses instead of blindly repeating an
effect. Only captured regular-file bytes and executable status are restored;
full filesystem metadata, crash recovery and automatic completion are absent.
Other platforms refuse native replacement.

The actual owned OS fixture proves the observer and its exposure refusals.
Private zero-agent controls exercise ingestion and restore accounting; neither
is a genuine production host issuer or real-agent acceptance. Tether's
provider-only handle and continuous writer admission producer remain separate
host integration requirements. See the consumer guide for acceptance boundaries.
