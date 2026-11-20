# Changelog

## [Unreleased]

## [0.1.0] - 2026-11-20

- First version: host-neutral steering for an agent turn.
  - `Situation` (with host `Signals`), `Slot`, `Selection` and a `Selector`
    interface, with `FocusSelector` (the window in front, the mode, a pinned
    slot, a fallback).
  - Modes and presets: `Preset` (slots, instructions, reflexes, routing rules,
    tool emphasis, output style, delivery policy, dispatch strategies), a layered
    `Registry` (shipped defaults < module < app) with `Check`, and a deliberate
    mode that only the `OwnerControls` returned by `New` can set. `Resolve`:
    deliberate mode, then a classified preset the policy applies, then the
    default; the default is not a mode.
  - Classifier policy: per facet, shadow, suggest or auto above a threshold
    (auto at least `AutoFloor`). `DefaultPolicy` applies nothing on its own;
    `act` and `affect` are data only.
  - Reflexes as configuration: text, predicate (libs agent/core reflexes), event
    and focus triggers; a `Vocabulary` of action kinds with remind and halt built
    in; cooldowns in a context-aware `CooldownStore` whose errors are reported
    (`MemoryCooldowns` with `Snapshot` and `Restore`); `Active` by preset; host
    signals as `signal.<key>` attributes, a `StateFill` and an agent id for
    predicates.
  - Routing: `MessageSubject` (the fields a libs core/util/router rule matches,
    with Surface's slash-command rule) and `ActiveRules`.
  - `Steer.Prepare` runs the pipeline and returns a `Turn` with a `Decision`
    whose `TraceFields` fill libs agent/trace's reserved fields without prompt
    content.
