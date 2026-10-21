# Device pairing, protocol 1

A reusable enrollment contract and SQLite mechanism for a standalone app with
native clients. Uses `security/auth` principals and bearer parsing. It does not
create a JWT issuer, grant application administration, or establish human identity.

## Contract

1. An app authenticates its administrator through its existing trusted admin
   surface. The administrator names a device and explicitly grants known scopes.
   `CreateGrant` receives the server-resolved owner ID, never a client assertion.
2. A grant contains 256 random bits, expires after five minutes by default (one
   hour maximum), and is stored only by hash. The app displays its code once.
3. The native client posts `{code, audience, scopes?}` to `/auth/pair/exchange`
   with `Hollis-Pairing-Protocol: 1`. Scopes can only narrow the grant; an omitted
   list retains it, and an empty list is invalid. Audience must match exactly.
4. Grant consumption and device creation commit atomically. Inactive, missing,
   replayed, wrong-audience and insufficient grants all return HTTP 401. Invalid
   JSON returns 400; unavailable storage returns 503. HTTP hosts must enforce a
   4 KiB body limit, strict decoding, a bounded listener-wide rate limit, exact
   Host/Origin policy, and `Cache-Control: no-store`. The library owns no HTTP server.
5. Success returns `{device, token, token_type:"Bearer"}`. This standalone token
   is opaque, contains 256 random bits, and is hash-only at rest. `device` has
   ID, owner ID, label, audience, explicit scopes, creation and expiry timestamps.
   Device lifetime defaults to thirty days and is configurable up to that bound.
6. `/auth/renew` accepts the current bearer and `{scopes?}`, returns updated
   device metadata, and retains the token. Renewal rechecks revocation, expiry
   and narrowing atomically. Expired/revoked clients must pair again. An app's
   authenticated admin surface lists/revokes devices and may revoke unused grants.

The protocol header is mandatory on exchange and renewal. Ordinary authenticated
app endpoints use `Authorization: Bearer`; app route scopes remain explicit.
Missing/invalid credentials return 401, missing route scope 403. Do not admit
browser cookies as device credentials or device credentials as admin sessions.

## Client custody and transport

Require HTTPS or literal loopback HTTP reached through an authenticated tunnel.
Tailnet membership alone does not turn plaintext HTTP into an HTTPS endpoint.
`Origin` validates and normalizes a root origin. Bind Keychain/secret entries to
that exact origin and reject all HTTP redirects. Never put codes or tokens in
URLs, logs, preferences, analytics or event payloads. Clear the code after exchange.
An origin change requires explicit pairing; never silently reuse credentials.

Lost successful exchange responses require a fresh grant. The store intentionally
cannot replay the plaintext token. The admin can revoke an unused device visible
in the list. Application observations use their own durable idempotency protocol.

## Standalone and mesh profiles

Pairing establishes device enrollment; it is separate from app-token issuance.
The standalone profile implemented here verifies local opaque credentials through
`auth.Verifier` and remains usable without a mesh issuer. The mesh profile keeps
the mesh authority's existing enrollment and scoped JWT issuance/JWKS verification.
Do not create an alternate mesh issuer, exchange an app-local device credential
for mesh privileges, or treat a device ID as the human actor's registry ID.

Existing enrollment may use an app-specific protocol and remote policy.
Adoption requires an explicit adapter/migration; these route names do not make
the protocols interchangeable. Existing grants/tokens are not migrated or revoked
by adopting this module. Local key provisioning remains a distinct
deployment workflow; it can adopt the enrollment semantics when needed.

## Store use and boundaries

Call `Open(db, Config{Audience: "example", Scopes: []string{"example.read"}})`
on an application-owned SQLite database (JSON functions and RETURNING required).
Configure its driver, busy timeout, and serialized writes. Namespace tables are
`hollis_pairing_*`; `Open` does not change application `user_version`. The library
registers no SQL driver. Tests use modernc SQLite.

`CreateGrant`, `List`, `Revoke`, and `RevokeGrant` require host-enforced owner/admin
authorization. `Device` resolves the authenticated device-to-owner association;
`Verify` implements `auth.Verifier`. `DeviceInTransaction` resolves credentials
inside the app's write transaction on the same database. The app must check scopes and device ownership
at each operation. List returns at most 200 records; the host should expose that
bound. Revocation affects the next verification; applications own cancellation
of long-lived requests and must not claim immediate stream revocation from this
store alone. Application writes should recheck credentials transactionally where
an in-flight revocation race matters. Cleanup/retention is host policy.

This version has no DPoP, client certificates, approval polling, credential
rotation, or cross-issuer trust federation. Those require explicit extensions.

Run `go test -race ./...` for expiry, replay, scope narrowing, ownership, revocation,
atomic mint rollback and concurrent consumption behavior.
