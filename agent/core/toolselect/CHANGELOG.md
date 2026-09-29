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
  Assignment). Deferred until `agent-contracts-leaf` is tagged.
