# Changelog

## v0.2.0 — 2026-10-20

Additive release: no existing exported signature changes.

### Added

- `Refresher` keeps a short-lived JWT (for example a 15 minute agent token) valid for the life of a process: it renews at 2/3 of the token's life, keeps serving the held token while the issuer is unreachable (until within `Skew` of expiry), backs off between failed mints, runs one mint at a time, and returns `ErrNoToken` (wrapping the mint error) when nothing valid is held. A mint is judged against the clock when it arrives, and `Token` stops waiting when its context is done. `HTTPMinter` is a token-endpoint client for Tether-style `POST /auth/token`; its errors carry the HTTP status at most, never the reply body, the URL or the credential, and it refuses cross-origin redirects.
- `Switch`, a swappable `Verifier`, and `Reloader`, which runs a caller-supplied all-or-nothing `Load` when a file's content changes (seen stable on two checks), on `SIGHUP`, or on `Trigger`; a failed or panicking `Load` keeps the previous configuration, and an edit made while `Load` runs is loaded again.
- `Guard.Apply(mode, verifier)`, `Guard.SetMode` and `Guard.CheckApply`, with `Config.Addr`: the posture dial and the verifier are replaced as one atomic step. They refuse an unknown mode, `enforce` with no verifier, and a mode `ValidateBind` rejects for `Config.Addr`. A request takes its mode and verifier once, on entry.
- Conformance vectors for `kind=service` JWTs (a far-future `exp`): they verify, remain valid years later, are rejected with `exp` zero or absent, are revoked by `jti` and by `sub`, and are scoped through the guard.
- README: the per-app posture dial (`HOLLIS_AUTH_<APP>`, then `HOLLIS_AUTH`), the service-token contract ("effectively non-expiring": a distant `exp` ended by revocation through the deny-list), the refresher and the reload recipe.

### Fixed

- `New` stored `Config.Mode` as given, so a non-canonical spelling such as `ENFORCE` matched no mode and let every request through. It now stores the canonical form, and a mode the guard does not recognize is treated as enforce.

### Notes

- Revocation takes effect within one poll interval (default 30s) after a successful poll. If the issuer or its deny-list is unreachable the last list stays in force (stale), so a revoked long-lived token keeps being accepted for as long as the outage lasts.

## v0.1.0 — 2026-10-15

Imported with history from the local go-auth repository. Bearer/API-key/JWT principals with scopes, a deny list, route guard that fails closed on unmapped routes, bind-address validation by mode (loopback, tailnet, unix socket), and a `Config.OnRequest` hook for request-scoped audit.
