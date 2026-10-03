# Host credentials and audit

The `capability/host` package is host-only infrastructure. The wire DTO and
catalog package stays independent of it. Plugins use connection-authenticated
stdio bindings. The bearer store accepts only verified agent clients, session
clients and MCP stdio proxies; it cannot issue plugin credentials.

Create a store for a random per-process host epoch and one exact audience. Feed
lifecycle-issued runtime identities through `ActivateOwner`. The store accepts
monotonic generations and refuses resurrection after fencing. It does not
allocate counters. The host must preserve lifecycle counters across controller
recreation. A new host process uses a new epoch/store.

Authenticate and approve the subject/control channel before `Issue`; claims are
trusted host inputs, not claims supplied in a request. Populate grant IDs,
capability names and their normalized effective scopes from reviewed live grants
and current policy, including each grant expiry in `GrantExpiresAt`. Identifiers
must be canonical and capability names use lowercase dot-separated operations.
Credential issuance does not create new grants.

The returned token has 32 cryptographically random bytes, encoded as unpadded
base64url. Server state keeps only SHA-256 hashes and authority metadata. Send
the raw token once through a private channel. Never send it via command-line
arguments, URLs, logs, registry documents, browser bundles or shared files. The
secret lives behind an opaque pointer; call `Reveal` exactly once at issuance,
then send it privately. Copies share that one-shot state. Formatting, JSON/text
encoding and slog redact the credential, including nested private fields. A
revealed string is still secret-bearing and must never be logged.

The default lease is five minutes. Configuration supplies an explicit maximum;
requested leases cannot exceed it. Expiry is also capped at the earliest grant
expiry and the renewal chain's absolute lifetime (`MaxLifetime`, default one
hour). A fresh authenticated issuance is required after that bound; renewal
cannot extend it even when the host supplies refreshed grant expiry metadata.
`Verify` checks the exact verified subject, audience, runtime identity, expiry
and active owner. It returns copied claims and a context cancelled on expiry,
revocation, renewal, generation replacement or shutdown. Expiry verification
uses wall-clock instants without monotonic readings, so system suspend counts
against the lease. Timers provide eager cancellation while running; verification
at dispatch/commit is authoritative after a wall clock jump or resume. The
injected clock permits deterministic expiry tests. Clock callbacks must run
asynchronously after the requested duration; timer stop must not wait for its
callback.

`Renew` requires an authenticated live owner/control channel, checked by the
adapter. It preserves subject, audience and incarnation, accepts only existing
grant IDs and narrowed scopes, rotates the token and cancels the original lease.
A refusal leaves the original live credential untouched. Recheck reviewed grant
liveness/current caller policy on renewal; this store cannot infer host policy.

Call `RevokeOwner` on lifecycle disable, stop or unload. It fences that
generation even before activation, while stale cleanup cannot revoke a newer
active incarnation. `Issue` returns a non-secret `LeaseID` stable across
renewals; retain it and call `RevokeLease` for single-client withdrawal without
a raw token. `RevokeSubject` disconnects one verified client and `RevokeGrant`
withdraws credentials referencing one grant, without fencing the owner or
revoking unrelated clients/grants. `Revoke(token)` is a current-token-only
convenience; it cannot revoke a renewed successor. Use `Close` on host shutdown.
These privileged methods belong behind host control authentication. Timer expiry
invalidates tokens and cancels leases; revocation state retains no raw token.

Lease contexts are cancellation signals, not atomic commit barriers. Verify
again after waiting and before committing an effect, under the host's lifecycle
admission/commit guard where necessary. Revocation cannot undo an already
committed write. Hosts must bound credential issuance and live owner inventory
according to their admission policy. The library creates no daemon or durable
credential database.

`AuditEvent` uses separate `Actor{kind,id}` metadata for plugins and initiating
callers, independent of the non-plugin bearer Subject. It contains timestamp,
request/trace IDs, actor/initiating caller, runtime identity,
capability/grant/policy IDs, target/server/tool, effect, outcome/effect state
and duration. It has no argument, content, token or raw error field. Supply only
verified safe identifiers. The host sink owns bounded telemetry buffering,
retention and presentation, and must return promptly and honor cancellation.
`Auditor.Record` isolates sink errors and panics, returning failure separately
and incrementing a thread-safe failure counter. Per-code/ reason denial counters
(`Denials`) are bounded to the error vocabulary, copied on read and updated even
when persistence or presentation fails. A sink failure never changes the
authorization decision. A missing sink is an explicit host configuration choice,
not a claim that telemetry was persisted.
