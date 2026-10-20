# Changelog

## Unreleased

Added `Switch` (swappable `Verifier`), `Reloader` (reload on file content change, `SIGHUP` or `Trigger`; failed loads keep the previous config) and `Guard.Apply` / `SetMode` / `CheckApply` plus `Config.Addr` (live posture dial and verifier replaced as one atomic step; refuses enforce without a verifier and modes `ValidateBind` rejects). Fix: `New` stored `Config.Mode` as given, so a non-canonical spelling such as `ENFORCE` matched no mode and let every request through; it now stores the canonical form, and a mode the guard does not recognize is treated as enforce. Each request takes its mode and verifier once on entry.

Added `Refresher` (keeps a short-lived JWT valid: renews at 2/3 of its life, serves the held token while the issuer is down, backoff, `ErrNoToken`) and `HTTPMinter` (token-endpoint client for Tether-style `POST /auth/token`). No change to existing APIs.

Conformance vectors for `kind=service` JWTs with a far-future `exp` (verify, still valid years later, `exp` required, revocation by `jti` and `sub`, scope handling through the guard). README documents the per-app posture dial (`HOLLIS_AUTH_<APP>` then `HOLLIS_AUTH`) and the service-token contract.

## v0.1.0 — 2026-10-15

Imported with history from the local go-auth repository. Bearer/API-key/JWT principals with scopes, a deny list, route guard that fails closed on unmapped routes, bind-address validation by mode (loopback, tailnet, unix socket), and a `Config.OnRequest` hook for request-scoped audit.
