# Changelog

All notable changes to go-hitl are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## v0.1.0 — 2026-09-29

### Added

- Initial pre-release build of the lifecycle contract, wire contract version `1.0`.
- `schema`: embedded JSON Schema (draft 2020-12) bundle of 36 definitions and a per-definition validator. Twelve definitions are copied unchanged from Tangent's `tangent.hitl-item` v1.0, three response profiles are tagged `x-hitl-tier: profile`, Core subsets relax the presentation-bearing outputs, and new definitions add `HITLTerminalConflictErrorV1`, `HITLErrorV1`, `HITLPlainErrorV1` and the responder/proof slot (`ResponderV1`, `ProofV1`, `ProofBindV1`).
- Root package `hitl`: `State` and `CanTransition`, `Handle`, strict Get/Await/Withdraw commands, `RetrievalResult`, the sealed `Outcome` union, typed errors with `DecodeError`/`EncodeError`, `Participant` with an optional `Proof{scheme, key_ref, binds}`.
- Root package `hitl`: `Store`, `Record`, `CheckSwap`, and a reference `Service` (idempotent enqueue, get, await, withdraw, respond, `ExpireDue`) with first-terminal-wins and atomic refusal of replies at or after `expires_at` even when no sweeper has run. `Get` and `Await` never mutate: past `expires_at` they return the expired view computed at read time and write nothing; only `Respond`, `Withdraw` and `ExpireDue` materialize expiry, once.
- `memstore`: in-memory `Store`.
- `hitltest`: fixtures, operation scenarios, `RunConformance`, `RunStoreContract`, `NewServiceAdapter`.
- Opt-in cross-check against Tangent's schema via `HITL_TANGENT_SCHEMA` (skips loudly when unset).
- `docs/CONTRACT.md`, `docs/MAPPING.md`, `docs/CONFORMANCE.md`.
