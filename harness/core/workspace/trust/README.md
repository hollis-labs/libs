# Explicit provider trust

`Preflight` freezes an explicitly authorized request and observes its supported
mechanism without changing directories, configuration, or lock files. Required
Codex, Antigravity, and unknown mechanisms return `Unsupported`; optional unknown
mechanisms produce explicit omission. There is no generic fallback writer.

The host supplies the stable final runtime cwd, its canonical existing parent,
the authorized shared configuration root, provenance and grant revision. The
complete lock set includes both the configuration root and runtime parent, with
a protected lock namespace outside each mutable root. The host callback refreshes
authority and fencing. No ambient home, environment or credential discovery occurs.

`Apply` requires verified candidate artifact root and generation evidence and the
single caller-owned `effects.ReceiptSink`. The root builds this sink over its
operation receipt store. All effects must preflight before artifact mutation;
the host applies credentials, repository attachments, then shared trust last.
This leaf neither publishes a workspace nor declares launch readiness. Shared
trust is retained on failure; there is no cross-effect rollback promise.

The Linux/macOS Claude port pins an `os.Root` for an existing owned configuration
directory and exclusively acquires `.claude.json.lock` after durable intent.
Contention returns a typed conflict; it never deletes a stale or foreign lock.
The lock descriptor remains open until conditional release, preventing inode
reuse from impersonating the owned lock. A changed root or lock is retained.
The final runtime directory may be absent, but its parent must resolve exactly;
symlink targets and failed resolution never receive fallback trust.

The writer reads at most 4 MiB, rejects malformed or duplicate-key JSON and
non-object project entries, and preserves unrelated raw JSON values, including
large numbers. It sets `hasTrustDialogAccepted` and
`hasCompletedProjectOnboarding` for the canonical final cwd. Already-present
trust does not rewrite the configuration. Replacement uses an exclusive private
temporary file, fsync, root-relative rename, and directory fsync. Errors after
replacement report `Partial`, even when the trust fields are visible.

Confinement assumes cooperating mutators hold the declared mutation locks.
Rechecks detect supported configuration, root and lock changes; descriptor
confinement does not protect against arbitrary same-user parent renames or
entry swaps. Providers ignoring these locks are outside that guarantee.

Receipts contain authorized paths, mechanism, grant identifiers and nonsecret
trust state. Configuration bytes, snapshots, and content hashes are never
returned or logged. Evidence comes from a trusted host; schema/operation/input
binding checks do not establish cryptographic authenticity. `Inspect` classifies
bound receipts without replay or cleanup; interrupted evidence remains
`Partial` or `Conflict`, even when trust is present. Trust grants no sandbox or
fabric permission. Root dispatch, publication, recovery scheduling and app
wiring remain caller responsibilities.
