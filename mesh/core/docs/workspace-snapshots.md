# Workspace snapshot events

`workspace.snapshot.taken` uses the shared `mesh.Event` envelope and the closed
`workspace.snapshot.taken.v1` payload, represented by `WorkspaceSnapshotTaken`.
`DecodeWorkspaceSnapshotTaken` validates bounded JSON and rejects unknown
versions/fields, duplicate or case-aliased keys, numeric overflow and trailing
values. A future incompatible or additive payload shape requires a new version;
readers must refuse an unsupported required version rather than discard fields.

The host mints a stable set ID per operation, binding the input and canonical
target-map digests, policy revision, run and instance. Each logical root/store
has its own tree/commit and observation interval. Failed roots retain a fixed
error code without provider error text or synthetic hashes. Skipped coverage is
reported as bounded reason/count pairs, never private path lists. Repository
HEAD/branch observations are optional and timestamped; absence means unavailable,
not a fabricated branch or commit. Root paths, file names and content stay outside
this event payload. Hosts still own redaction and private event access; validation
cannot detect arbitrary secrets hidden in caller-supplied identity strings.

Source journal positions are explicit: `journal.after` is exclusive,
`journal.through` inclusive, and both bind `journal_id`. Zero denotes an actual
initial source position. `Event.Cursor` is the destination event store's replay
cursor and is never converted into a shim cursor. Envelope generation/sequence,
controller epoch, host binding fence, boot generation and runtime generation
remain distinct counters/identities. Event correlation binds the set; causation
binds its operation; session binds its run.

Publish only after the capture result AND its retention references are durably
recorded under current host authority. A payload or event does not establish
that ordering, grant capture authority, mint pins or prove quiescence. Capture
requires an explicit finite capture/storage/run-budget policy, effective grants,
mandatory exclusions and store custody. Nil provider means disabled; KEEP and
pins dominate GC. Failed/uncertain recording retains obligations and cannot emit
a successful observation by assumption.

On reattach, replay the real journal first. At a supported held quiescent boundary,
take one host-policy capture and publish one coalesced event with its uncaptured
cursor interval. Never fabricate per-tool events for the detached interval. If
an active provider cannot be held consistently, defer or report a best-effort
observation with `complete=false`; timestamps alone do not make it an atomic cut.
Per-root intervals remain distinct even at an admitted boundary: no cross-root
atomic filesystem snapshot is implied. `complete` describes the host's admitted
coverage and is not proof available from this pure schema validator. Mandatory secret/Git
metadata exclusions lie outside selected eligible scope: `excluded` and
`git_metadata` counts may remain informational on an otherwise complete admitted
result. Oversize, unsupported, unobserved content or failed roots always makes
coverage incomplete. The host must bind those exclusions to the selected policy,
not let event data decide what was eligible.

Emission requires continuous verified fence/custody admission and actual bound
capture/retention receipts. No automatic shim capture, live runtime integration,
restore, fork or garbage collection is performed by this contract package.
