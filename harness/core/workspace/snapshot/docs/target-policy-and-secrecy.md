# Snapshot scope and confidential ingestion

Snapshot policy belongs to the host. A shim supplies execution and journal
boundaries; it does not choose capture scope, storage budgets or retention.

`DeriveTargets` selects a defensive, digest-bound target plan from resolved
filesystem **write grants** and host root bindings. Directory requests do not
become grants. Root IDs and provenance remain bound even when equal physical
roots are captured once. Nested roots are captured separately without also
being ingested through their parent.

`TargetPolicy.OptIn` narrows existing grants. `OptOut`, effective deny/protect
rules, source-read scope, provider credential/state paths and explicit
`CredentialPaths` exclude content. Boot, state, runtime and scratch content is
excluded by default; an explicit nonsensitive scratch selection still cannot
override mandatory exclusions. An empty eligible selection is unsupported,
never the legacy convention that an empty path list means the whole root.

## Production availability

`NewGuardedProvider` validates finite policy and remains effectless. It now
requires an opaque actual OS observation, a separate genuine current host
admission port and a version-bound redaction adapter. Missing or stale support
returns an unavailable/refused error before ingestion. The Linux observer checks
an already-frozen provider-only group, namespaces, physical roots and protected
store/control reachability through mounts, proc, inherited descriptors and file
mappings. It performs no process or freezer operation. Other platforms have no
supported observer yet. See the package README for the concrete public seam.

A requested confinement mode, a backend name, same-user ownership or mode
`0700` cannot establish confidentiality from an agent running as that same
user. The host must independently hold actual submission/source-writer and
in-flight-I/O fences, validate current principal/policy/binding/incarnation and
reconcile the complete protected-root inventory. Neither an OS observation nor
a callback returning nil grants that authority.

Actual owned zero-agent OS controls demonstrate observation and exposure
refusals. The private kernel exercises ingestion and durable accounting with
disposable filesystem/Git fixtures. Neither supplies the genuine production
host producer, which remains a separate integration requirement. Physical and
host custody must remain held through ingestion, replacement and accounting;
unsupported guarded capture has no production fallback.

The existing `ShadowGit` provider and its four-method snapshot port remain
available for their original callers. Passing `TargetPlan.Targets()` to raw
`ShadowGit.Capture` does **not** enforce the guarded plan's credential
exclusions, budgets or custody. There is no automatic capture during workspace
materialization and no production fallback from unsupported guarded capture.

## Before Git receives candidate bytes

The guarded kernel first reserves finite capture, root, storage and run
budgets under the stable admission lock. Missing or zero required limits
leave capture disabled. Retention defaults to KEEP; exhaustion refuses or
defers a new capture while preserving pending work and pins.

Eligible files are copied into a private mirror through descriptor-relative,
non-following native opens. Selected symlinks, special files, root replacement,
empty coverage, changing files and exceeded traversal/content limits refuse
before that mirror is offered to Git. Metadata for known credential sources
is refreshed before each attempt so an ordinary-looking hardlink to such a
file is excluded too. Supported native file-opening safeguards are Linux and
macOS; other platforms refuse confidential ingestion.

Mandatory name exclusions include `.env*`, credential/provider configuration
and key/token paths. They are intentionally conservative. They are **not** a
secret-value detector: a secret copied into an otherwise ordinary source file
is not discoverable from its filename alone. Hosts must supply the full known
credential path inventory and an appropriate eligible content policy. The
guarded production path additionally invokes its bounded, version-bound
redaction adapter before mirror writes or Git ingestion. This adapts known
host-resolved values; it does not promise arbitrary-secret or universal PII
detection.

Only regular eligible mirror content reaches Git. Inherited Git control
variables, global/system configuration and initialization templates cannot
redirect guarded ingestion or install ambient template content. The guarded
kernel verifies real tree and commit objects before admitting a retained set.
Plaintext limits, bounded traversal and actual retained-store accounting are
separate checks; budget failure does not remove uncertain objects to manufacture
successful admission.

## Receipts and events

`CaptureResult.Description()` is defensive diagnostic data. It is not an
isolation capability or event authority. `Retained()` succeeds only after
verified ingestion, durable accounting and initial journal pinning.

The admitted account digest binds the exact capture intent, target-map and
policy identity, actual retained set, byte accounting and a sanitized
`RetainedManifest`. That manifest persists native observation intervals,
per-root stable store IDs and hashes, fixed outcome codes and skip counts.
It contains no raw paths, provider errors or secret content.

Completeness is relative to the bound eligible policy. Informational
`excluded` and `git_metadata` counts may accompany a complete set. An eligible
`oversize_untracked`, `unsupported` or `unobserved` omission prevents a complete
claim. Root failure in the guarded capture path leaves a pending reservation
and no retained receipt; an attempt/failure observation must not be published
as `workspace.snapshot.taken`.

A host event mapper must acquire the retained set's journal read lease and use
its verified `Intent`, `Manifest` and `Set` while the lease remains held. It
must independently validate the current host/controller binding before
persistence and publication. A decoded description or manifest cannot mint a
read lease. Closing a lease retains its durable pin; only an admitted explicit
owner-completion operation releases that reference. Pins dominate retention.

There is no automatic TTL, in-place whole-root restore, agent adoption or live
activation in this layer. Whole-tree fork and pin-aware retention are separate
host operations, and their custody and backend requirements remain explicit.
