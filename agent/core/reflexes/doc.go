// Package reflexes is a database-agnostic sense, integrate, act steering
// engine: the reflex engine extracted from Nanite. A Reflex evaluates a
// JSON trigger (predicate, event or interval) over a windowed State and,
// when it fires, stages or applies an action of a named kind. Fired
// reflexes are arbitrated per action kind (deny_overrides,
// first_applicable or all_applicable) under a system, kind, reflex
// cooldown cascade, and each firing emits one trace record.
//
// # Shape
//
// The host supplies small consumer-defined seams: Source (candidate
// reflexes), KindCatalog (action-kind facets), optionally StateSource,
// TraceStore and Filters. Engine.Run is the whole pipeline; Resolve,
// EvaluateTrigger, EmitFirings, EffectiveCooldown and RecentlyFired are
// exported for hosts that compose their own.
//
// The act side is a registry rather than a fixed switch: Executor.Handle
// registers an ActionHandler per kind with a Phase (before or after the
// trace write), and Executor.Stage marks kinds whose effect is a decision
// the caller acts on (returned in Result.Applied). The library imports
// nothing from any application.
//
// # Conjunctions and unknown signals
//
// Predicates should be conjunctions over several signals; a single
// detector false-positives. The canonical case is healthy idle compression
// versus a cache-miss echo attractor: both show "output shorter than
// prior", but only the echo also has input_tokens near 3, cache_read 0,
// identical output and zero tool calls.
//
// Numeric signals are plain ints where 0 means unknown or none. A host
// that does not report cache reads makes cache_read_window = 0 true.
// Author predicates as conjunctions, and expect a later version to add
// explicit "known" flags.
//
// # Trigger kinds
//
// A trigger_kind is "predicate", "event" or "interval". Predicate nodes
// (the kind field) are AND, OR, tool_calls_window, cache_read_window,
// input_tokens_window, output_growth_window, regex_match_window,
// user_regex_window, text_regex_window, entity_mention_window,
// tool_name_window, envelope_type_window, mail_unread_count,
// identical_output_window, prefix_pressure, scope_tier, execution_pattern
// and attr. See EvaluateTrigger.
//
// ScopeTier and ExecutionPattern are live scalar signals supplied by the
// host. Their predicates compare case-sensitive strings with = (default),
// == or !=; unknown operators return false without an error. Missing or
// non-string values become empty strings, so an unset signal matches an
// empty value. There is no history-window or cold-start guard.
//
// Firing traces mirror populated ScopeTier and ExecutionPattern as top-level
// scope_tier and execution_pattern fields, omitted when empty. They are not
// inserted into Attrs. Generic attr predicates and the trace's copy of Attrs
// retain their host-defined semantics.
package reflexes
