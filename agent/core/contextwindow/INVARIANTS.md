# Slot system invariants (mechanism level)

> **Scope.** This file states what the `contextwindow` mechanism itself
> guarantees: the slot table, `ContextWindow.Assemble`, and the cache-key
> helpers. It does not restate the invariants that depend on how a consumer
> feeds and consumes the mechanism (dispatch flavors, cache-marker placement,
> permission and skills renderers).
>
> Those consumer-side properties come from the Nanite application, where this
> package was extracted from. Nanite's `internal/service/slot_invariants_test.go`
> remains the full seven-invariant enforcement point after adoption. It now
> asserts against this module's exported values instead of locally defined
> ones. This repository has no dispatcher and cannot replicate it.
>
> **Provenance:** CW-20260512-0124 (SP-20260512-0011 Wave 4).

If a change needs to alter one of the guarantees below, update this file in
the same commit as the test change.

---

## M1. Stable slot order (seed INV1)

**Guarantee.** `SlotOrder` is a fixed sequence of 15 slot names, and
`Assemble` walks it directly. Blocks come out in exactly that order, every
turn, for every `ContextWindow`; slots with no content are skipped but never
reordered. Every name in `SlotOrder` has an entry in both `DefaultBudgets()`
and `DefaultCompactable()`, and those maps have no other entries.

**Why it matters.** Positions are cache-load-bearing. A slot drop, insertion
or reorder shifts the cacheable prefix consumers build from the assembled
blocks and produces cache-miss patterns that are hard to attribute. Earlier
slots are the ones that change least, so their position is what makes
prefix caching pay off. `SlotUniversal` sits at position 0 for the same
reason.

**A missing map entry is silent.** Budgets and compactability are read with
plain map lookups. A slot absent from `DefaultBudgets` gets `MaxTokens: 0`
(unbounded) and one absent from `DefaultCompactable` gets `Compactable: false`
(never compacted): the wrong default for both, with no error.

**Enforced here by.**

- `TestSlotOrder_MatchesSeedSnapshot` pins `SlotOrder` to a hardcoded
  15-element literal.
- `TestDefaults_MatchSeedSnapshot` pins the default budgets, compactability,
  `SlotHandoffMaxTokens`, `BudgetFraction`, `DefaultContextWindowSize` and the
  four compaction stage names and order.
- `TestDefaults_CompleteForSlotOrder` checks that both default maps cover
  `SlotOrder` exactly.
- `TestAssemble_ordering` and `TestSlotHandoff_RegisteredInOrder` (ported
  seed tests) check assembled order.

Changing a pinned literal is a deliberate, reviewed act, never a fix-up to
make a test pass.

**Not enforced here.** Cross-flavor identity of the sent shape, and
Anthropic's `cacheable_prefix_tokens` math, live in the consumer.

---

## M2. Agent instructions ship inline (seed INV5, first half)

**Guarantee.** `SlotAgent` is exempt from the per-slot truncation ceiling
in `Assemble` (`name != SlotAgent`). Agent instructions ship whole even when
they exceed their `MaxTokens` budget. Every other slot over its ceiling is
truncated with a `[truncated]` suffix, on a UTF-8 rune boundary, and its cache
key is recomputed.

**Why it matters.** An agent must receive its role and boot instructions
before it can use tools to orient itself. The exemption is deliberate, not an
oversight to clean up.

**Enforced here by.** `TestTruncateSlot` (ported seed test) covers truncation
of ordinary slots. The seed has no mechanism-level test for the `SlotAgent`
exemption itself; Nanite's `TestSlotInvariants_AgentInstructionsInline`
covers it end to end.

---

## M3. Deterministic cache keys (seed INV5, second half, mechanism part)

**Guarantee.** `ComputeCacheKey` is the SHA-256 hex digest of the content, so
identical content yields identical keys across runs and processes. Identical
`SetContent` calls on a fresh `ContextWindow` yield byte-identical `Assemble`
output.

`Assemble` mutates `PrevHashes` and `CacheHits` as a side effect. Call it once
per turn: a second call without an intervening `SetContent` sees every slot as
unchanged, so its `Changed` values differ from the first call's. This is
documented behavior, not a bug.

**Not in this package.** The pointer/stash envelope for oversized slots, its
content-addressed artifact IDs, and the atomicity rule (stash before pointer
ships) belong to the consumer's broker. This package only supplies the
deterministic keys such a scheme builds on.

**Enforced here by.** `TestComputeCacheKey_deterministic`,
`TestAssemble_cacheHits`.

---

## M4. The mode slot is a fixed point in the table (seed INV4, table part)

**Guarantee.** `SlotMode` keeps its position (between `SlotAgent` and
`SlotRules`), its 500-token budget and `Compactable: false` in the constant
tables. It is covered by M1 like any other slot.

**Not in this package.** That `SlotMode` carries empty content and is skipped
at assembly is a property of what the consumer writes into it; this package
does not force the slot empty. The consumer that seeded this package
currently never populates it.

**Enforced here by.** `TestDefaults_MatchSeedSnapshot` and
`TestSlotOrder_MatchesSeedSnapshot`.

---

## Consumer-side invariants that remain in Nanite

| Seed invariant | Property | Why it is not enforced here |
|---|---|---|
| INV2 | Universal slot at position 0 on every dispatch flavor, sourced from the universal-rules block | Needs the dispatcher and the universal-rules renderer |
| INV3 | Cache markers only on the stable-prefix slots, never on dynamic slots | Provider-adapter behavior |
| INV6 | Permission summary renders path constraints | Permission renderer |
| INV7 | Skills listing is name plus description in its own slot, no bodies | Skills renderer |

Position 0 for `SlotUniversal` is still guaranteed here structurally (it is
the first element of `SlotOrder`, pinned by M1); what is not guaranteed is
that a consumer fills it.

## How to evolve the contract

1. Update the paragraph above with the rationale in the same commit as the
   test change.
2. Adding a slot means appending to `SlotOrder`, adding entries to both
   default maps, and updating the pin literals in `slot_pin_test.go`; it is
   a breaking change for every consumer's cache prefix and needs a CHANGELOG
   entry that says so.
3. In the consumer's repository, update any cache-marker priority list
   derived from `SlotOrder` in lock-step.
