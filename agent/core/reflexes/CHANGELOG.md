# Changelog

All notable changes to go-reflexes are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## Unreleased

### Added

- Package `reflexes` (standard library only), lifted from Nanite's
  `internal/agent/reflexes` at Nanite HEAD `0eaf01d1`: `Reflex`, `ActionKind`,
  `State` (with `Attrs`), `MessageSignal`, `EventSignal`, `AppliedAction(s)`,
  `CandidateOutcome`; `EvaluateTrigger` (predicate, event and interval
  triggers); `Resolve` (deny_overrides, first_applicable, all_applicable, with
  the fail-open-to-all_applicable kind lookup); `EffectiveCooldown` and
  `RecentlyFired`; `EmitFirings` and the unified trace record.
- `Engine` with `New`, `Run`, `RefreshKinds` and `KindDefaultRecurrence`:
  one pipeline replacing four hand-copied list, filter, cooldown, resolve,
  emit implementations. `RunInput` covers their differences (`Kinds`,
  `ExcludeKinds`, `Candidates`, `State`, `Extra`, `BeforeEmit`,
  `SkipFilters`, `SessionID`); `Result.Considered` is the count of candidates
  left after `Kinds` and `ExcludeKinds`.
- Consumer-defined seams: `Source`, `KindCatalog`, `StateSource`, `TraceStore`,
  `Filters`. `New` rejects nil `Source` and `KindCatalog` with `ErrNilSeam`.
- `Executor` with a handler registry replacing the fixed action switch:
  `Handle` (with `WithPhase`: `PhaseResolve`, `PhaseAfterEmit`), `Stage`,
  `Apply` and `AfterEmit`; `ActionHandler`, `HandlerFunc`, `Firing`. The seven
  Nanite kinds are pre-registered; `resume_loop_run` needs an app-registered
  `PhaseAfterEmit` handler.
- New generic `attr` predicate over `State.Attrs`. There are no aliases for
  Nanite's `scope_tier` and `execution_pattern` predicate kinds and no
  special-cased keys: a host translates its own vocabulary to `attr`. The
  trace record carries a copy of `State.Attrs` as `attrs`.
- Tests: the store-free evaluator (12), resolve (10) and recurrence (9) tests
  ported with type substitutions; fake-based engine, executor and telemetry
  tests; one `Run` equivalence test per replaced pipeline with byte-for-byte
  trace goldens; a race test for concurrent `Run` and `RefreshKinds`;
  `FuzzEvaluateTrigger`.

### Changed

- The plugin filters and observers interface is `Filters` (Nanite's
  `PluginHooks`), with the `WithFilters` option, `FilterState`,
  `FilterAction`, `Fired` and `Staged`. Renamed from `Hooks`/`WithHooks`
  before the first tag: `github.com/hollis-labs/go-hooks` exports `Hook` and
  `hooks`, a reflex is conceptually a hook implementation, and this library's
  plugin-filter seam must not collide with it. This module does not import
  go-hooks.
- `State.ScopeTier` and `State.ExecutionPattern` are replaced by the generic
  `State.Attrs`; Nanite's adoption sets them as ordinary entries and rewrites
  its `scope_tier` / `execution_pattern` predicates to `attr`.
- `Executor` is usable as a zero value; an unregistered kind other than the
  built-ins is an `unknown action_kind` error, including `resume_loop_run`
  (Nanite treated it as a no-op).

### Not carried over

- Nanite's `StateCollector`, seeds, SQL, opt-out rules, `EnsureSeeds` and
  `Validate`. The "more than one winner on a first_applicable kind" warning
  from the dispatch path is not ported: with the cached kind catalog a
  degraded lookup already fails open with its own warning.

### Known limitations

- Numeric signals are ints where 0 means unknown, so `cache_read_window = 0`
  is true for a host that does not report cache reads. Documented, not
  changed.
