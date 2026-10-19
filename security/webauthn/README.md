# WebAuthn

WebAuthn ceremony and public-credential storage building blocks extracted from
Cerberus. Uses `go-webauthn/webauthn` v0.18.2 for protocol validation. No private
keys, bearer credentials, identity-provider transport, HTTP routes or approval
policy are supplied by this module.

`Party` supplies stateless, user-verification-required assertion and registration
primitives. `Service` adds durable single-use registration/assertion/removal
ceremonies, five-minute maximum ceremony lifetime, atomic counter updates and
recorded-assertion re-verification. `sqlitestore` implements the storage contract;
`presencetest` supplies an ephemeral software authenticator for tests.

## Trust and ownership

Construct a party from a trusted RP ID and origin allow-list. Initialize each
subject through `Store.CompareAndSwap` with a stable, opaque `User.ID` and display
names. Subject identity and all enrollment/removal authorization come from the
application. In particular, `BeginRegistration` and `FinishRegistration` must
sit behind the application's first-key bootstrap or existing-key authorization.
A browser-supplied subject or snapshot is never trusted input. Every Service
begin/finish call also requires a nonempty caller binding from authenticated
server context, such as the authenticated session identifier. Store only its
hash; never use a browser-supplied binding or a bearer secret. A wrong binding
is rejected without spending the legitimate caller's pending ceremony. Legacy
pending ceremonies without a binding must be restarted.

`BeginAssertion` takes the exact challenge bytes from the caller's proof profile.
The caller binds the operation, audience, revision, human, decision, unpredictable
nonce and expiry before passing the bytes in. The library's random ceremony ID
is not automatically included in those supplied bytes. `Challenge` preserves the
length-prefixed Cerberus challenge format; it is not a JCS implementation or the
operation-proof draft's domain-separated binding hash. The store permanently
reserves each assertion/removal challenge digest across its entire authority,
including across subjects. Reusing a challenge is rejected even after expiry,
removal, pruning or restart. This is an extra replay guard; a fresh unpredictable
nonce in the operation binding remains required. The ledger is a separate table,
not an ever-growing field in each subject snapshot. It must not be pruned while
credentials from that authority could still verify historical assertions.
Retire the ledger only when the authority's old credentials can no longer be
accepted, including through recovery, re-enrollment or restored backups.

A successful `FinishAssertion` means the assertion, UV, current credential and
counter verified and the ceremony/counter/receipt transaction committed. It does
not authorize the effect. A failed write returns no proof. Concurrent updates
fail closed; begin a fresh ceremony after a conflict instead of blindly replaying
a mutation. A begin may be retried after reloading state, using a fresh nonce
and fresh challenge. Invalid assertions spend their ceremony when that write
succeeds. Any failed transaction, including an enrollment ownership conflict,
rolls back the entire change; callers must not assume its ceremony was consumed.
An idempotent prepare endpoint must retain its nonce, ceremony ID and options
and return them on retry, or explicitly start a new attempt with a fresh nonce.
Repeating Begin with the old challenge cannot recover a lost successful response.

`Reverify` loads a previously recorded assertion, checks its exact expected
challenge, expiry and current credential, and verifies the signature again.
It does not advance the counter for that already-seen assertion. Counterless
credentials still have single-use ceremonies. Removal invalidates receipts and
pending eligibility for the removed key. Application revocation/trust checks and
atomic once-only continuation remain required at the effect boundary. There
is intentionally no separate ConsumeProof helper: spending a receipt in this
store cannot atomically record a different application's grant/execution intent.
Consumers must test that repeated delivery returns the original continuation
result and never creates a second grant or credential.

The SQLite store is an integrity boundary owned by its application. It does not
protect against a process able to rewrite the database or its trusted identity
configuration. Scope each store to one RP/trust authority, secure the database
and backups, and use authenticated authority/status transport between services.
Snapshots contain public credentials, ceremony state and assertion evidence;
they should not be exposed as management API responses. The caller owns request
size limits, rate limits, subject creation and retention policy. Expired pending
ceremonies/receipts are pruned on the next begin for that subject. The application
must limit outstanding ceremonies per subject and rate-limit begins to bound
snapshot size and challenge-ledger growth. It must deny removal of a last key
unless its explicit recovery policy permits that outcome.

## SQLite

Open a database using the application's supported SQLite driver, configure
connection/busy-timeout behavior, and pass the `*sql.DB` to `sqlitestore.New`.
The store creates only its own tables and does not open files or migrate another
application's registry. Version checks and credential ownership constraints
commit in the same transaction. `SQLITE_BUSY`, cancellation and other storage
errors refuse completion; they never downgrade to in-memory success. Configure
WAL and busy_timeout according to the application's concurrency requirements;
SQLite busy/busy-snapshot errors can occur even with version checks. Any CAS
error denies completion and must not trigger a retry that executes effects.

The challenge-ledger addition backfills challenges still present in old
snapshots when they are next updated. It cannot reconstruct history already
pruned by an earlier build. The module is unreleased; an experimental caller
upgrading persisted state must backfill all subjects before accepting requests
and account for missing historical challenges before trusting old counterless
credentials. Lazy per-subject backfill alone does not prevent cross-subject reuse
of an old challenge belonging to a subject that has not yet been updated.

SQLite is the only supported Store implementation. Any future implementation
must demonstrate atomic version/counter/receipt updates, credential ownership,
authority-wide challenge uniqueness after expiry/removal/restart, and reservation
rollback on failed transactions before consumers rely on it. The root package's
SQLite tests exercise these required behaviors; Service alone cannot enforce a
Store implementation's integrity guarantees.

## Cerberus integration

Cerberus can use `Party` while retaining its v2 sealed assertion, approval policy,
file-registry tamper detection, audit and local enrollment rules. That consumer
adapter is intentionally separate from switching its storage to SQLite. Import
history is preserved from `internal/presence`; application-specific policy stays
with Cerberus. No passkeys are imported or re-enrolled by this extraction.

## Validation

Run `GOWORK=off go test -race ./...`. Tests use temporary SQLite databases and
ephemeral synthetic authenticators. They exercise database reopen, cross-subject
ownership, atomic completion, replay, expiry boundaries, wrong origin/RP,
missing UV, bad signatures, counter regression, counterless assertions,
credential removal and commit failure. They do not replace a full WebAuthn
conformance suite or the consuming application's proof-profile tests.
