# Changelog

## Unreleased

- Extract UV-required WebAuthn assertion/registration validation and the synthetic
  authenticator from Cerberus with source history preserved.
- Add a credential-store interface and SQLite implementation with atomic state
  version checks and credential ownership constraints.
- Add durable single-use ceremonies, counter persistence, removal invalidation,
  and re-verification of recorded assertions against current credentials.
