# Changelog

## Unreleased

Added `Switch` (swappable `Verifier`), `Reloader` (reload on file content change, `SIGHUP` or `Trigger`; failed loads keep the previous config) and `Guard.SetMode` plus `Config.Addr` (live posture dial; refuses enforce without a verifier and modes `ValidateBind` rejects). `Guard` now reads its mode atomically and each request uses one mode snapshot. No change to existing call sites.

Added `Refresher` (keeps a short-lived JWT valid: renews at 2/3 of its life, serves the held token while the issuer is down, backoff, `ErrNoToken`) and `HTTPMinter` (token-endpoint client for Tether-style `POST /auth/token`). No change to existing APIs.

Conformance vectors for `kind=service` JWTs with a far-future `exp` (verify, still valid years later, `exp` required, revocation by `jti` and `sub`, scope handling through the guard). README documents the per-app posture dial (`HOLLIS_AUTH_<APP>` then `HOLLIS_AUTH`) and the service-token contract.

## v0.1.0 — 2026-10-15

Imported with history from the local go-auth repository. Bearer/API-key/JWT principals with scopes, a deny list, route guard that fails closed on unmapped routes, bind-address validation by mode (loopback, tailnet, unix socket), and a `Config.OnRequest` hook for request-scoped audit.
