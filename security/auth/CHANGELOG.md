# Changelog

## Unreleased

Tests and docs only; no behaviour change. Conformance vectors for `kind=service` JWTs with a far-future `exp` (verify, still valid years later, `exp` required, revocation by `jti` and `sub`, scope handling through the guard). README documents the per-app posture dial (`HOLLIS_AUTH_<APP>` then `HOLLIS_AUTH`) and the service-token contract.

## v0.1.0 — 2026-10-15

Imported with history from the local go-auth repository. Bearer/API-key/JWT principals with scopes, a deny list, route guard that fails closed on unmapped routes, bind-address validation by mode (loopback, tailnet, unix socket), and a `Config.OnRequest` hook for request-scoped audit.
