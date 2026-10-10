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
restore nothing. An authorized guarded restore and a captured-tree fork into a
new owned root remain separate integrations.

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
release has no production enforced-isolation, deletion or owner-completion
issuer. Public protected construction remains Unsupported. The callable private
kernel is verified in disposable zero-agent fixtures; it is not live GC.

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
