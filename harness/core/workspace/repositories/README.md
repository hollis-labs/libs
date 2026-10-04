# Authorized repository attachments

This leaf receives explicit repository, shared Git metadata and attachment-base
roots, host authorization, provenance, physical identities and the complete held
lock set. It imports no workspace root and discovers no homes or settings.
`Preflight` is observational; `Apply` requires verified candidate artifact receipts
and the existing `effects.ReceiptSink`. Root dispatch remains the caller's work.
All effects preflight before mutation; application order is credentials,
repository attachments, then shared trust. Completion grants no launch readiness.

`ResolveBranch` evaluates only fixed named variables once. Requests and receipts
retain the concrete validated branch and full pinned commit ID. Unresolved or
colliding branches refuse; creation has no detached fallback. `InspectResume`
accepts the original frozen request and bound receipt after restart. It preserves
dirty work and legitimate HEAD advancement while retaining the original base as
provenance. Missing or mismatched completed attachments report `Conflict` and
retain; interrupted effects remain `Partial`. Inspection never creates, resets,
cleans or switches branches.

New worktrees require an owned private base and an absent direct child. Existing
user checkouts require a separate explicit write grant. Existing readonly
attachments require a host sandbox proof covering their path and common metadata;
a path flag or chmod is not enforcement. Private checkout creation remains
`Unsupported` in the concrete adapter. Modes never silently substitute for one
another.

The direct Git adapter takes an explicit absolute executable, uses bounded local
arguments/output/timeouts, disables global/system configuration and hooks, and
refuses configured filters, includes, partial clones and shared object alternates
before checkout or safety inspection. Network protocols and lazy object fetching
are disabled. It exposes no fetch, clone, force, reset, clean, prune or branch
deletion operation. Direct commands keep implicit parent creation and broad
cleanup unavailable, without adding a module dependency.

Every parent must already exist and be canonical and safely owned. Creation
exclusively makes only the authorized leaf directory under a pinned existing
parent, with private permissions; failed creation retains that directory and
records uncertainty. Root custody and authority are rechecked after callbacks.
Confinement assumes cooperating mutation locks and private base custody. It does
not promise atomic protection against arbitrary same-user renames or swaps.
Linux/macOS are supported; other platforms refuse before mutation.

Retirement requires separate current removal authorization, exact accepted
shipped HEAD and unused-resource proofs. Complete safety inspection includes
tracked, untracked and ignored files, unknown or populated private Git metadata,
Git locks, unreachable commits and ahead counts. Index flags that hide tracked
changes (assume-unchanged or skip-worktree) retain as unknown; they are never
cleared. Private HEAD logs are read through the pinned metadata root, including
both old and new OIDs and the recognized ORIG_HEAD pseudoref. Their commits
must remain reachable through common branch, remote or tag refs after removal;
no preservation refs or shipped proofs are fabricated. Reads cap at 4 MiB,
1,024 log records and 256 distinct commit IDs. Missing, changed, malformed,
oversized or unrecognized private history retains. Any unknown or failed check
retains. `Retire` rechecks after durable intent and immediately before non-forced
Git removal, keeps the branch, and never deletes folders generically. The
concrete adapter observes safety again after its final authorization callback,
before removal, so that callback cannot hide late work behind a stale snapshot. User
checkouts are never retired. `InspectRetirement` binds the proof revisions and
classifies receipts without replay or cleanup. Publication, use-pin enforcement,
retirement scheduling and recovery execution remain caller responsibilities.

Receipts contain authorized paths, identities, ownership, branch/base/HEAD and
nonsecret grant/proof revisions. Git output, file lists and raw error text stay
private. Host binding checks are not cryptographic authenticity. Actual or
uncertain mutations and persistence failures retain explicit obligations through
the sole receipt sink. Terminal receipt recording uses a cancellation-detached
five-second cap, optionally shortened by `ApplyContext.CleanupTimeout`. Ports,
authority callbacks and sinks must honor contexts; arbitrary blocking callbacks
cannot be safely preempted. The root must preserve earlier recovery obligations
across retries.
