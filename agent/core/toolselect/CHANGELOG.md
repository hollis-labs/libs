# Changelog

All notable changes to go-toolselect are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## v0.2.0 — 2026-09-29

### Added

- Package `toolselect/launch`: `FromAssignment` derives a per-launch
  `profile.Profile` from an Assignment's `grants.mcp` `{allow, deny}` ceiling
  and a base Profile, never wider than the base. Depends on
  `github.com/hollis-labs/agent-contracts-leaf` v0.1.0; it is the only package
  that does.
- Known limitation: overlapping glob-versus-glob allow entries are dropped rather
  than intersected, so some legitimate combinations narrow more than strictly
  necessary (never wider than the base). Grants name tools, not servers.

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
- No claim of behavioral equivalence with the ranking in go-toolbroker or
  Nanite: their scorer tests were not ported (tracked as Torque
  CW-20260929-0017).
