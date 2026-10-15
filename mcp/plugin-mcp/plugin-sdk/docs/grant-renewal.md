# Host-owned grant scopes and renewal

`Grant.Scope` is strict, non-null, portable opaque JSON. It is not the
`capability.Scope` normalized exact-match authority envelope. A host may issue a
separately named/versioned descriptor, such as `host.nanite.readonly.query`, and
carry its existing typed scope without changing the shared `readonly.query`
schema. The host must decode the exact approved descriptor version, validate its
closed fields and limits, and enforce it at every endpoint. Discovery is not
authorization. `Catalog.ValidateScope` validates the normalized envelope; it
cannot validate an arbitrary host descriptor.

For example, a Nanite descriptor can retain `resources`, the exclusive
`session_ids` or `all_sessions` choice, and `include_content`. Here
`all_sessions` means the host's current workspace, not a wildcard identifier.
Context descriptors retain `source_ids`, the same session choice and their
query/body limits; wake and reflex descriptors retain exact reviewed slugs/IDs.
The SDK supplies no interpretation, default authority, aliases, wildcard
semantics or Nanite endpoint implementation. Existing declaration metadata and
its version remain unchanged; the host owns the explicit declaration-to-grant
mapping and authoritative typed decoder.

## Opt-in renewal

A plugin implements `subprocess.GrantsRenewalHandler` (TypeScript
`grantsRenewed`) to accept discovery replacement. The host explicitly offers
`grants_renewal_version: 1` in Init parameters. Serve advertises
`grants_renewal_version: 1` in its Init result only when both the offer and that handler are present. Older
plugins remain supported but cannot accept this renewal method. The host must
require the acknowledgement before attempting renewable operation; it must not
infer support from a capability declaration or use another Init.

The host sends an ordinary JSON-RPC **request**, never a notification:

```json
{
  "jsonrpc": "2.0",
  "id": 8,
  "method": "plugin/grants/renew",
  "params": {
    "renewal_version": 1,
    "sequence": 1,
    "incarnation": {"host_instance": "host", "owner_id": "plugin", "owner_generation": 1},
    "grants": [],
    "context": {"timeout_ms": 1000}
  }
}
```

The example IDs, tuple and observer budget are placeholders, not authority or
recommended policy. Real values come from the live host. The response echoes
`renewal_version`, `sequence` and `incarnation`. Request IDs are positive safe
integers; sequence starts at one and advances once per accepted attempt on that
connection. The context is finite and carries no reverse binding. Its budget
starts at physical receipt, and the old grant expiry additionally clips the
callback. The bounded control lane and existing frame/queue limits apply.

The update must retain grant order, IDs, capability names, schema versions,
**exact scope bytes**, audience, policy revision and runtime tuple. Only
`issued_at` and `expires_at` advance. A live old lease is required both before
and after the callback. Expired, revoked, unloaded or disconnected state cannot
be revived. The SDK does not choose a grant duration or authenticate policy.

The authored handler atomically replaces its own cached discovery metadata,
respects cancellation, and returns only after acceptance. The SDK separately
installs a detached snapshot. `GrantSetFromContext`/`currentGrantSet` returns
current SDK discovery after successful Init. New request-scoped HostClients use
the accepted snapshot; already-created clients and in-flight calls retain their
original finite expiry/deadline bounds. Renewal never extends an admitted call.

## Host transaction and uncertainty

The host serializes renewal with revocation and dispatch ownership:

1. Revalidate the accepted bundle, current policy, exact authority and live
   runtime/old lease. `GrantSet.ValidateRenewal` checks unchanged authority and
   live timestamps; it cannot authenticate the host or establish policy.
2. Send one bounded request while the old lease remains live. Require an actual
   successful terminal response with the expected version, sequence and tuple.
3. Revalidate the same old lease, tuple and policy after the response, then
   atomically commit the replacement in the host's serialization domain.
   Cancel old in-flight permits conservatively before admitting new permits.
4. On timeout, callback failure, malformed/error/late response, lost ack,
   disconnect or policy drift, revoke and stop the child. **Do not retry** or
   leave potentially divergent state active.

Child callback success alone is not physical acknowledgement. A peer may have
applied discovery before its reply becomes unavailable. The SDK fences failed
callback transitions and transport failures; the host still owns conservative
revocation/stop and the final commit decision. No cross-process atomic commit
or rollback is implied. The host may renew a finite 24-hour grant after current
approval, but that duration and revalidation are host policy, not SDK defaults.

`CredentialStore.Renew` renews host client credentials without extending their
underlying grant expiry. `host/bindings/renew` renews reverse bindings. Neither
replaces a child's GrantSet and neither substitutes for this acknowledged
transaction.
