# Changelog

## [Unreleased]

## [0.1.0] - 2026-11-20

- First version: host-neutral steering for an agent turn.
  - `Situation`, `Slot`, `Selection` and a `Selector` interface, with
    `FocusSelector` (the window in front, the mode, a pinned slot, a fallback).
  - Modes and presets: `Preset` (slots, instructions, reflexes, routing rules,
    tool emphasis, output style, delivery policy, dispatch strategies), a layered
    `Registry` (shipped defaults < module < app), and `Modes`, which only the
    owner may set. `Resolve`: deliberate mode, then a classified preset the
    policy applies, then the default.
  - Classifier policy: per facet, shadow, suggest or auto above a threshold.
    `DefaultPolicy` applies nothing on its own.
  - Reflexes as configuration: text, predicate (libs agent/core reflexes), event
    and focus triggers; a `Vocabulary` of action kinds with remind and halt built
    in; cooldowns; `Active` by preset.
  - Routing: `MessageSubject` (the fields a libs core/util/router rule matches)
    and `ActiveRules` (a preset switches rules on).
  - `Steer.Prepare` runs the pipeline and returns a `Turn` with a `Decision`
    whose `TraceFields` fill libs agent/trace's reserved fields.
