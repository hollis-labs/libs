# Changelog

## Unreleased

## v0.1.0 — 2026-10-19

- Require Go 1.26.9 for the standard-library security fixes identified by the
  release vulnerability scan.
- Require authenticated caller binding at Service begin/finish; wrong-session
  completion is refused without consuming the legitimate caller's ceremony.
- Permanently reserve challenge digests per authority in SQLite to reject
  counterless replay across ceremonies, expiry, key removal and process restart.
- Extract UV-required WebAuthn assertion/registration validation and the synthetic
  authenticator from Cerberus with source history preserved.
- Add a credential-store interface and SQLite implementation with atomic state
  version checks and credential ownership constraints.
- Add durable single-use ceremonies, counter persistence, removal invalidation,
  and re-verification of recorded assertions against current credentials.
