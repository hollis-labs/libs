# reflex-definition-spec

Portable contract for one reflex definition: validates the record shape, the trigger predicate grammar and per-kind action_spec; hosts implement the engine.

## Status

**Pre-release.** This project is unreleased, not deployed, and has no outside consumers. It's being built in the open: the code, the docs, and this README describe what exists today, not a pitch for what's planned. Interfaces and behavior change without notice, and there are no compatibility guarantees yet.

## Install

```sh
go get github.com/hollis-labs/reflex-definition-spec
```

## Usage

```go
package main

import (
	"fmt"

	reflexspec "github.com/hollis-labs/reflex-definition-spec"
)

func main() {
	d := reflexspec.Definition{
		Name:        "idle-drift",
		TriggerKind: "predicate",
		// "tool_call_window" is one letter short of "tool_calls_window".
		TriggerSpec: `{"kind":"tool_call_window","window":3,"op":"=","value":0}`,
		ActionKind:  "inject_reminder",
		ActionSpec:  `{"body":"State the next concrete action.","urgency":"info"}`,
	}
	for _, err := range reflexspec.Validate(d) {
		fmt.Println(err)
	}
}
```

The same program lives in [`examples/hello`](./examples/hello/main.go). It prints
`trigger_spec.kind: unknown predicate kind "tool_call_window"; the engine would treat it as never firing [unknown_predicate_kind]`.

## Why

go-reflexes evaluates a reflex's `trigger_spec`, but validating one is outside its scope (its README and AGENTS.md say so). Nanite's own `validateReflexDefinition` checks only that `trigger_spec` parses as JSON. A predicate node with a misspelled kind therefore passes there, and at run time the engine returns "unknown predicate kind", which its caller treats as "did not fire". The reflex is stored, active and inert. This package walks the predicate tree and rejects that.

## What it checks

- `Validate(Definition, ...ValidateOption) []error`: required `name`, the `trigger_kind` grammar (`predicate`, `event`, `interval`), both specs, the shape of `action_kind`, `status` and `provenance_tier`, and `recurrence_override_seconds >= 0`. `nil` inherits the kind's default cooldown and an explicit `0` means no cooldown; they stay distinct through JSON.
- `ValidateTriggerSpec`: for `predicate`, every node's `kind` against `PredicateKinds()` (`AND`, `OR` and 14 leaf kinds), each kind's required fields, types, operators, modes, scopes and RE2 patterns, and fields the kind does not define (the engine ignores those, so a typo silently falls back to a default). For `event`, a non-empty `name`. For `interval`, `every_n_ticks >= 1`.
- `ValidateActionSpec`: a JSON object for every kind. `inject_reminder` needs `body`; `dispatch_to_agent` needs `agent_slug` (`confidence` within [0,1]); `resume_loop_run` needs `loop_run_id`; `halt_session` has an optional `reason`. `force_tool_choice`, `add_schedule`, `send_message` and any kind this package has not heard of get the object check only.
- Open enums: `action_kind`, `status`, `provenance_tier` and event names are shape-checked. `WithKnownActionKinds`, `WithKnownStatuses`, `WithKnownProvenanceTiers` and `WithKnownEvents` inject a catalog for strict checking. `DocumentedActionKinds()` lists the seven kinds go-reflexes and Nanite name.

Errors are `*Error{Field, Code, Message}`; `Code` values are constants (`CodeUnknownPredicateKind`, `CodeRequired`, ...). The [`conformance`](./conformance) package embeds valid and invalid fixtures, each invalid one recording the `(field, code)` pairs it must produce and why, and a reference runner. They are Go-only; no other-language runner exists.

## Verified vs read

Recorded 2026-09-29, against Nanite HEAD `6443d0fd` and go-reflexes v0.1.0. Nothing under `apps/` or `libs/go-reflexes` was modified.

- Run: Nanite's `validateReflexDefinition` was executed on a scratch copy of the tree (a throwaway test calling it with an in-memory store) with `{"kind":"tool_call_window",...}`, a `regex_match_window` with no `pattern`, `{"kind":"AND","clauses":[]}` and `{}` as `trigger_spec`. It returned no errors for all four. This validator rejects all four.
- Run: `BaseSeeds()` and `LoomPilotReflexSeeds()` were copied out and executed to capture the 21 seeded rows, marshalled as the seeder does; they are in `testdata/nanite_seeds.json`. All 21 `action_spec` values are accepted. Six seed triggers use Nanite's `scope_tier` / `execution_pattern` kinds, which go-reflexes replaced with `attr`; this package targets go-reflexes' grammar and rejects them as unknown kinds (`TestNaniteSeedTriggersAgainstGoReflexesGrammar`).
- Read, not run: go-reflexes' `evaluator.go` predicate switch and defaults, `types.go`, its README and AGENTS.md; Nanite's `store/reflex_taxonomy.go` and `handleValidateReflex`.
- Not claimed: behavioural equivalence with Nanite's validation or with go-reflexes' evaluator. This package is stricter than both by design (it rejects unknown fields and values the engine would silently default), and the strictness is chosen, not measured against the engine on real definitions.

## Known limitations

- Targets go-reflexes' grammar. A host still on Nanite's `scope_tier` / `execution_pattern` must translate them to `attr` before validating.
- The grammar is a copy. `TestGoReflexesVocabularyDrift` compares kind names only, and only when a go-reflexes checkout sits at `../go-reflexes` (or `GO_REFLEXES_DIR`); it skips otherwise, so CI does not run it. Field lists, operators and defaults are not compared.
- No host-defined predicate kinds: there is no way to add a kind to the grammar. A host with its own kinds cannot use `ValidateTriggerSpec` as is.
- Stricter than the engine: unknown fields, `window` below 1, non-integral integers, an unrecognised `entity` even when `pattern` overrides it, and an event trigger name containing whitespace are all rejected although the engine would run them.
- `urgency` is observed as `info` or `warn` in every seed but nothing in Nanite or go-reflexes consumes it; only its shape is checked. `body` is required because every seed has one; its consumer was not traced.
- The unit of `every_n_ticks` is undocumented (`State.TickN`'s producer was not traced); no upper bound is enforced.
- `force_tool_choice`, `add_schedule` and `send_message` have no evidenced fields and get the object check only.
- Nanite's PATCH endpoint treats `recurrence_override_seconds: 0` as "clear the override"; go-reflexes, and this package, treat `0` as an explicit no-cooldown. The difference is in Nanite's API, not in the stored shape.
- Regex patterns are compiled with Go's `regexp`; that checks syntax only, not that the pattern means what its author intended.

## Compatibility

This module is pre-1.0: minor releases may break the exported API. Pin an exact
version, and read [CHANGELOG.md](./CHANGELOG.md) before upgrading, since every
breaking change is listed there. The `Definition` struct duplicates the field names and
JSON tags of go-reflexes' `Reflex` and Nanite's `store.AgentReflex` on purpose and imports
neither; keeping the three in step is a manual job.

## Out of scope

- The reflex engine: evaluating triggers, dispatch, cooldown, filters. That is go-reflexes.
- Provenance-tier enforcement. Which tier may declare which action kind is a live table in Nanite, authorization policy rather than shape. `provenance_tier` is only pattern-checked, or checked against a catalog you inject.
- Hook-to-reflex ingest.
- Any store, SQL, opt-out semantics or approval workflow: Nanite product behaviour.
- Inventing fields for `force_tool_choice`, `add_schedule` and `send_message`.
- An authoring UI, a CLI, a standalone `.schema.json` file, cross-language fixtures, or adoption by Nanite or any other host.

## Development

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
go test -run '^$' -fuzz FuzzValidate -fuzztime 20s .
```

CI (`.github/workflows/check.yml`) is the full gate.

## License

MIT — see [LICENSE](./LICENSE).
