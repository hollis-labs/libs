# Guarded capture and selective restore

The public mechanism accepts observed physical isolation and independent host
admission. The library does not supply a production host authority issuer.
Unavailable admission refuses without ingestion or restore effects; the
private fixture constructor is not a fallback.

`ObserveIsolation(ctx, IsolationRequest)` observes an already-frozen,
provider-only Linux cgroup. It does not freeze or launch processes. The request
binds a process ID and start time, the actual group descriptor, store, target
plan and complete protected control-path inventory. Other platforms refuse
this observation. The returned opaque `IsolationProof` has `Verify`, `Digest`,
`ProtectedDigest` and `Close`; closing it releases observation descriptors,
not the process freeze or host fence.

`NewGuardedProvider(GuardedConfig)` requires this issued proof, a `SnapshotHost`,
finite capture policy, bound targets and a `ContentRedactor` with a revision.
Construction does not create the ledger or Git objects. `CaptureBound` acquires
host admission before initialization and ingestion. Each `SnapshotOperation`
binds the isolation and protected-inventory digests, store, capture intent,
request digest and redaction revision. The host must reconcile the protected
inventory with its actual current control and journal roots; a digest cannot
prove that an omitted root is protected.

`SnapshotHost.AcquireSnapshot` returns a held `SnapshotAdmission` with `Verify`,
`Record` and `Close`. These are host implementation obligations, not authority
conferred by satisfying a Go interface. Record must durably bind the exact
operation and outcome; Close must retain uncertain writer exclusion rather
than unconditionally thawing the provider.

## Two independent admission requirements

An OS capability must describe the actual canonical process and namespace,
bound source roots and private store. Store and control content must be
inaccessible through child mounts, inherited file descriptors and reachable
process descriptors. A PID, directory mode `0700`, requested sandbox policy,
backend name or advisory file lock cannot establish that exclusion.

Current host effect authority is separate. It must bind the principal,
effective policy, run and instance, binding and controller epoch, operation,
target map and store. The host must hold its submission and writer admission
fence through the operation. A valid OS observation does not grant permission
to capture or replace content. Missing, stale or unsupported physical custody
or host authority refuses; a decoded receipt or caller assertion supplies
neither.

Capture keeps finite root, capture, storage and run budgets in force before
candidate content reaches Git. Effective write grants determine eligible
targets; directory requests do not enlarge them. Credential exclusions and
redaction must act before index or object ingestion. Empty effective coverage
refuses rather than selecting the whole root. The existing
[scope and secrecy guide](target-policy-and-secrecy.md) describes eligibility,
sanitized manifests and policy-relative completeness.

## Compare and replace under one fence

A selective restore names each target and relative path, the retained set and
tree, and the expected current presence and content hash. Empty selections
must not become a whole-root overwrite. The mechanism must validate every
selection before replacement, including physical root and path identities,
then preserve those checks through each admitted effect.

Call `RestoreSelective(ctx, retained, RestoreRequest)` with an issued
`RetainedSet`, bound `CaptureIntent` and explicit `RestoreSelection` entries.
Each selection carries `ExpectedFile{Present, SHA256}`. Present files require
their current SHA-256; absent files require an empty hash. A conflicting
selection returns `ErrRestoreConflict`. This operation restores captured
regular files; it does not delete a current file or restore an entire root.
`RestoreResult` reports the operation, outcome, changed count, partial status
and obligations. An unissued provider or receipt returns refusal and cannot
claim an acquired pin.

The current-content read, atomic replacement and durable accounting must occur
under the **same genuinely held writer and custody fence**. A last-moment hash
check cannot prove compare-and-swap when another writer can change the file
between that check and replacement. Rename atomicity alone cannot close this
gap. A host must supply actual exclusion of relevant writers or the operation
must refuse as unsupported.

A conflict discovered before effects preserves all selected content. Results
must distinguish a refused operation from one that already changed content.
Late authority revocation, cancellation, custody loss or accounting failure
after a possible effect must preserve the uncertainty instead of reporting
an unchanged or complete operation.

## Retention and retry

Use the issued retained-set receipt and hold its read pin while validating and
using captured bytes. Diagnostic descriptions and deserialized manifests are
not admission capabilities. KEEP remains the default, and pins dominate
retention.

An uncertain operation retains its pin, evidence and recovery obligations.
Neither an error nor a missing acknowledgement authorizes deletion or blind
retry. Recovery must inspect the original bound operation, current authority,
actual content and durable accounting before deciding how to continue. It
must not renew a stale proof or overwrite a conflicting edit to manufacture
success.

## Evidence boundaries

Consumer controls must reach the public mechanism and demonstrate refusal
before effects for absent or stale authority, custody loss and hash conflicts.
A racing-writer control must exercise the actual fence; testing only a final
hash comparison cannot establish safe replacement.

Positive owned-fixture controls must identify their actual OS construction,
held exclusion and persisted receipts. A private test issuer, a policy boolean
or a model of callback ordering does not demonstrate production custody. On
an unsupported platform or unavailable host producer, preserve the explicit
refusal and state which positive behavior remains untested.

Source-level controls do not establish real-agent capture, restore, event
publication, owner completion, garbage collection or live adoption. Those
outcomes require the supported host producer and their own affected acceptance.
