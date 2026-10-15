# Changelog

This module is versioned independently with `security/policy/vX.Y.Z` tags.

## v0.1.0 - 2026-10-15

First release under `github.com/hollis-labs/libs/security/policy` (folded in from the substrate repository `policy` module with its history; the substrate path no longer exists).

### Added

- Neutral request, decision and mandatory approval/dry-run/rate obligation types.
- Swappable PDP, explicit per-call enforce/shadow and failure policy, and one
  synchronous evaluation audit event with no request context or exception text.
- Caller-owned recorded-case conformance scaffolding and contract tests.

- `permission` (with `permission/pathgrants` and `permission/summary`): the shared permission contract, moved here from `harness/interception/permission` so that the harness and the agent can both depend on it without depending on each other. Imported with its history; the substrate import path `.../harness/interception/permission` no longer exists.

No engine adapters or consumer wiring are included.
