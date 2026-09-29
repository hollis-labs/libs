# Changelog

All notable changes to go-toolselect are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## v0.1.0 — 2026-09-29

### Added

- Package `toolselect`: `Tool`, `Catalog`, `Index` (`NewIndex`, `Rank`), the
  one-shot `Rank`, `Rule` / `Match` / `Action` with `ActionInclude`,
  `ActionExclude` and the new `ActionOrder`, `MatchTier`, `Hit`, and the
  options `WithK1B`, `WithMaxResults` and `WithStopwords`. BM25 with a total
  order: pinned, then exact name, then prefix, then score, then name.
- Package `toolselect/profile`: `Evaluate` with `Catalog`, `Server`, `Tool`,
  `Profile`, `VisibleTool`, `HiddenReason`, `HiddenCause`, `ErrUnknownServer`
  and `ErrBadPattern`. Precedence is server disabled, deny, read_only, allow.
- Not included: `toolselect/launch` (per-launch profile derivation from an
  Assignment); it was deferred until `agent-contracts-leaf` was tagged
  (v0.1.0 now is) and lands in a later release.
- No claim of behavioral equivalence with the ranking in go-toolbroker or
  Nanite: their scorer tests were not ported (tracked as Torque
  CW-20260929-0017).
