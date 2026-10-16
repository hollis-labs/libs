# go-context-window

Fixed, ordered context-slot system with per-slot budgets, cache-key tracking and escalating compaction.

It is not: an Anthropic cache-marker placer, a prompt renderer, or a place to
"tidy" the slot table. It holds the ordered slots, their default budgets and
compactability, the window that assembles them, the compaction pipeline, and
the handoff and overflow helpers built around them.

## Start Here

- `contextwindow` package — the importable API; its `doc.go` is the package documentation.
- `slot.go` — slot names, `SlotOrder`, `DefaultBudgets`, `DefaultCompactable`. The most sensitive file; read Boundaries first.
- `INVARIANTS.md` — the mechanism-level guarantees and which tests pin them.
- `example_test.go` — the runnable examples; the README `## Usage` fence is a complete program in the same shape.
- The repository-root workflows run the whole `agent` module gate.

## Commands

```sh
scripts/check agent
```

Run from the repository root. CI uses the same module check.

## Boundaries

- **`SlotOrder` is cache-load-bearing.** It is a fixed sequence of 15 names
  and `Assemble` walks it directly. Never reorder, alphabetize, regroup,
  insert into, or drop from `SlotOrder` or the slot-name constant block in
  `slot.go`, and never "clean up" its interleaved comments in a way that moves
  a name. Any such change shifts every consumer's cacheable prefix and
  silently breaks prompt-cache economics; it compiles and passes a naive
  suite. Nothing else guards it.
- Guarded by: `TestSlotOrder_MatchesSeedSnapshot` (hardcoded 15-element
  literal), `TestDefaults_MatchSeedSnapshot` (default budgets, compactability,
  constants, stage names and order), `TestDefaults_CompleteForSlotOrder`, and
  the ported `TestAssemble_ordering` and `TestSlotHandoff_RegisteredInOrder`.
  Editing a pinned literal to make a test pass is the failure mode, not the
  fix; changing the order is a deliberate breaking change with a CHANGELOG
  entry.
- Adding a slot means: append to `SlotOrder`, add entries to both
  `DefaultBudgets` and `DefaultCompactable` (a missing entry silently becomes
  unbounded and non-compactable), update the pin tests, and note in the
  CHANGELOG that it breaks consumers' cache prefixes.
- `SlotAgent` is deliberately exempt from the truncation ceiling in
  `Assemble`. Do not "fix" it.
- `Assemble` mutates `PrevHashes` and `CacheHits`, so a second call without
  `SetContent` in between reports every slot unchanged. Document, do not fix.
- The four compaction stages run in a fixed escalating order (drop
  enrichment, dedupe tool results, summarize oldest, strip tool blocks);
  `TestDefaults_MatchSeedSnapshot` pins the names and order.
- The compaction event ID is an opaque random hex string, not a UUID. Do not
  reintroduce `google/uuid` or add a shape check on it.
- Dependencies are `substrate/llm-core/llmtypes`,
  `substrate/llm-core/llmcontracts` and `yaml.v3` only.
  Every dependency's own `go` line must stay at or below this module's.
- Do not add references to symbols or files in any consuming application.
- No `replace` directive in `go.mod` and no committed `go.work`: consumers cannot resolve either.
