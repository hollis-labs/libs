// Package contextwindow defines the named slot substrate that is assembled
// into each LLM turn: slot identities, ordering (`SlotOrder`), default
// budgets, default compactability, and the `ContextWindow` that holds the
// per-turn slot store, plus the escalating compaction pipeline, handoff
// payloads and context-overflow classification built around it.
//
// Adding a new slot requires explicitly appending it to `SlotOrder`,
// `DefaultBudgets` and `DefaultCompactable` here, and, in the consumer's own
// repository, updating any cache-marker priority list it derives from
// `SlotOrder`. There is no fallback for a slot missing from either default
// map. A slot missing from a map silently becomes unbounded and
// non-compactable.
//
// # Invariants
//
// The order of `SlotOrder` is cache-load-bearing: reordering, inserting or
// omitting a slot changes the cacheable prefix consumers build from the
// assembled blocks. The mechanism-level guarantees this package makes (order
// stability, the agent-inline exemption, pointer determinism, mode-slot
// inertness) are documented in INVARIANTS.md alongside this package.
//
// Provenance: CW-20260512-0124 (SP-20260512-0011 Wave 4).
package contextwindow
