# Changelog

This module is versioned independently with `mesh/gatecrasher/vX.Y.Z` tags.

## Unreleased

- Add operation-bound coordinator over existing policy evaluation and a single
  authoritative HITL owner, with verified response authorization and native
  option/scope fidelity.
- Add durable journal ports for pinned request intent, recoverable mapping,
  delivery leases, uncertain-send reconciliation and separate execution reports.
- Add real HITL behavioral/race fixtures, restartable fake adapters and an example.
- Require Go 1.26.11 and the pending mesh/core v0.11.0 foundation; publication,
  clean consumer resolution and independent review remain landing prerequisites.
