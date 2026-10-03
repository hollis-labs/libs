# Changelog

## Unreleased

- Added a separate Node-only `/build` export for manifest-v2 staged artifact
  hashing, canonical writing and verification, with Go/Node shared vectors.
  Worker Serve protocol remains independently versioned.
- Breaking: protocol-2 strict Init, required grants/capability contract/incarnation,
  typed handshake failures and initialization before handlers. Harness options
  use `grants` and `incarnation`. Reverse/hooks offers are validated and declined.
- Shared Go/TS Grant and Init fixtures, portable scope-number limits, Unicode
  surrogate rejection and v2 replay corpus; historical v1 observations retained.

## 0.1.0

Initial stdio protocol 1 runtime, optional capability interfaces, instance-local
config and redacting stderr logger, and direct/JSON-roundtrip author test harness.
Shared Go/TS conformance transcripts and Node/Deno lifecycle acceptance.
