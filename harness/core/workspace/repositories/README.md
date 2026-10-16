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

## Clone attachments

A `clone` request carries a frozen `CloneIntent`: `required` or `preferred`
copy on write, and for `preferred` an optional explicit worktree-fallback
authorization. At the workspace root that authorization must be its own granted
repository effect on the shared Git metadata (common) root; the clone's grant
never implies it. `Preflight` probes the actual source/destination filesystem
pair before any mutation and selects the construction; `Apply` probes again and
refuses `clone_capability_changed` rather than switching modes.

| Situation | Result |
|---|---|
| Copy on write available, source at clean base | `clone`, method `reflink` (Linux) or `clonefile` (macOS) |
| `required`, no method | `Unsupported` (`clone_unsupported`), nothing created |
| `preferred`, no method, no fallback authorization | `Unsupported` (`clone_unsupported`), nothing created |
| `preferred`, no method, fallback authorized | registered `worktree`; receipt records the clone request, method `none`, the reason and the fallback authorization |
| Capability unknown (probe error) | `Refused` (`clone_capability_unknown`) |
| Failure after the clone directory exists | `Partial` with `recovery_required`; the directory is retained |

Stable no-method reasons include `reflink_unsupported`, `clonefile_unsupported`,
`cross_filesystem`, `probe_fixture_unsupported`, `platform_unsupported`,
`source_not_at_base`, `source_dirty`, `source_index_flags`,
`submodules_unsupported`, `partial_clone_unsupported`, `tree_too_large`,
`tree_listing_unavailable`, `index_listing_unavailable` and the
`object_store_*` family.

The concrete adapter builds an independent repository, never a copy of a
`.git` file or directory: `git init` with an empty template and the source's
object format; immutable loose objects and complete pack/index sets cloned file
by file from the source's object store (no hardlinks, no alternates, no copied
config, index, refs or hooks; derived indexes and keep markers are skipped,
promisor packs refuse); the concrete branch created at the full pinned base
with a create-only ref update; tracked regular files cloned from the source
working tree, symlinks written by `checkout-index` into absent paths, and a
private index built by `read-tree`. A clone is only made when the source's HEAD
is the pinned base and it has no tracked change or hidden index flag; a live
dirty or moved source is never used to manufacture a clean base. A refresh then
rehashes every file and a clean status, including ignored files, proves the
tree equals the base. Untracked and ignored source files (for example `.env`)
are never cloned. `.git` is tightened to `0700`. Observation proves `.git` is a
real owned directory that is its own common directory, distinct from the
source's, with no alternates or refused configuration and no registration as a
source worktree.

The Linux probe clones into an unnamed `O_TMPFILE` inode beneath the private
attachment base, so no directory entry is created. The macOS probe creates one
randomly named fixture there and removes it. Each probe opens the source file
read-only and checks it is the regular file it observed.

Limits: real copy-on-write construction is unmeasured in this repository's
checks, which run on ext4 (FICLONE answers `EOPNOTSUPP`). The positive path is
exercised only with a test-only cloner whose receipts record `fixture_copy`,
which is never copy on write. Submodules, partial clones, alternates and
repositories whose bounded listings exceed 4 MiB answer no method. Commit
connectivity of the base is checked; full object connectivity is not.
Removing a clone is `Unsupported` (`clone_retirement_unsupported`) and retains
it; a worktree fallback retires through the worktree rules.

## Merge-back

`CheckMergeBack` and `ApplyMergeBack` fast-forward one source branch to the
head of a completed copy-on-write clone. This is its own
`repository_mergeback` effect with its own authorization; attachment creation,
the clone grant and capture grants never authorize it. The request names the
target branch and its exact current value (empty when it must be absent). The
clone must still be on its attachment branch. A checked-out target, a moved
target or clone head, or a head that does not descend from both the base and
the prior target refuses. The adapter proves ancestry in the clone before any
transfer, fetches only the clone's branch from its local path with object
checking, no tags, no FETCH_HEAD, no submodules and no automatic GC, proves the
head and ancestry again in the source, refreshes authority, rechecks the
target, and moves the ref with a compare-and-swap. Nothing is forced, no
worktree or index changes, and a fetch that ran is reported as mutation even if
the ref update did not happen. Worktree fallbacks share the source's refs and
have no merge-back.

The direct Git adapter takes an explicit absolute executable, uses bounded local
arguments/output/timeouts, disables global/system configuration and hooks, and
refuses configured filters, includes, partial clones and shared object alternates
before checkout or safety inspection. Network protocols and lazy object fetching
are disabled. It exposes no clone, force, reset, clean, prune or branch
deletion operation, and its only fetch is the explicit merge-back from an
attachment's local clone path. Direct commands keep implicit parent creation and broad
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
