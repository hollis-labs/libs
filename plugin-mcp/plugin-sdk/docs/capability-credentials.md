# Host credentials and audit

The `capability/host` package is host-only infrastructure. The wire DTO and
catalog package stays independent of it. Plugins use connection-authenticated
stdio bindings. The bearer store accepts only verified agent clients, session
clients and MCP stdio proxies; it cannot issue plugin credentials.

Create a store for a random per-process host epoch and one exact audience.
Feed lifecycle-issued runtime identities through `ActivateOwner`. The store
accepts monotonic generations and refuses resurrection after fencing. It does
not allocate counters. The host must preserve lifecycle counters across
controller recreation. A new host process uses a new epoch/store.

Authenticate and approve the subject/control channel before `Issue`; claims
are trusted host inputs, not claims supplied in a request. Populate grant IDs,
capability names and their normalized effective scopes from reviewed live
grants and current policy. Credential issuance does not create new grants.

The returned token has 32 cryptographically random bytes, encoded as unpadded
base64url. Server state keeps only SHA-256 hashes and authority metadata. Send
the raw token once through a private channel. Never send it via command-line
arguments, URLs, logs, registry documents, browser bundles or shared files.
Formatting the returned credential redacts it; directly reading `Token` is
still secret-bearing and must be handled deliberately.

The default lease is five minutes. Configuration supplies an explicit maximum;
requested leases cannot exceed it. `Verify` checks the exact verified subject,
audience, runtime identity, expiry and active owner. It returns copied claims
and a context cancelled on expiry, revocation, renewal, generation replacement
or shutdown. The injected clock schedules expiry even without a subsequent
verification call. Clock callbacks must run asynchronously after the requested
duration; timer stop must not wait for its callback.

`Renew` requires an authenticated live owner/control channel, checked by the
adapter. It preserves subject, audience and incarnation, accepts only existing
grant IDs and narrowed scopes, rotates the token and cancels the original lease.
A refusal leaves the original live credential untouched. Recheck reviewed grant
liveness/current caller policy on renewal; this store cannot infer host policy.

Call `RevokeOwner` on disable, stop, unload, disconnect or policy withdrawal.
It fences only the matching incarnation, so late cleanup cannot revoke the
replacement. Use `Revoke` for individual withdrawal and `Close` at host
shutdown. Timer expiry invalidates tokens and cancels leases. No raw token is
retained for revocation.

Lease contexts are cancellation signals, not atomic commit barriers. Verify
again after waiting and before committing an effect, under the host's lifecycle
admission/commit guard where necessary. Revocation cannot undo an already
committed write. Hosts must bound credential issuance and live owner inventory
according to their admission policy. The library creates no daemon or durable
credential database.

`AuditEvent` contains timestamp, request/trace IDs, actor/initiating caller,
runtime identity, capability/grant/policy IDs, target/server/tool, effect,
outcome/effect state and duration. It has no argument, content, token or raw
error field. Supply only verified safe identifiers. The host sink owns bounded
telemetry buffering, retention and presentation, and must return promptly and
honor cancellation. `Auditor.Record` isolates sink errors and panics, returning
failure separately and incrementing a thread-safe failure counter. A sink
failure never changes the authorization decision. A missing sink is an explicit
host configuration choice, not a claim that telemetry was persisted.
