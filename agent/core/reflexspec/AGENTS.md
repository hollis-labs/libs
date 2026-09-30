# reflex-definition-spec

Portable contract for one reflex definition: validates the record shape, the trigger predicate grammar and per-kind action_spec; hosts implement the engine.

It is not a reflex engine, a JSON Schema file or an authorization layer. go-reflexes evaluates reflexes and hosts own their policy; this repo only says whether a definition is well-formed.

## Start Here

- `reflexspec.go` — `Definition`, `Validate`, `Error`, the options. `trigger.go` — the predicate grammar as data (`leafRules`) and the walk. `action.go` — per-kind `action_spec` checks.
- `conformance/` — the `go:embed` fixture tree (`testdata/<valid|invalid>/<case>/{input.json,want.json}`) and the reference runner. `testdata/nanite_seeds.json` — the 21 real seeded rows.
- `examples/hello/main.go` — the runnable example; the README `## Usage` fence must stay identical to it.
- `.github/workflows/check.yml` — the full CI gate; `release.yml` refuses a tag with no CHANGELOG heading.

## Commands

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## Boundaries

- No `replace` directive in `go.mod` and no committed `go.work`: consumers cannot resolve either.
- Standard library only, and no hollis libs, go-reflexes included (`TestStdlibOnly`). The predicate vocabulary is copied into `leafRules`; `TestGoReflexesVocabularyDrift` compares kind names with a sibling checkout and skips when there is none. Do not import go-reflexes to remove the copy without a decision.
- The grammar targets go-reflexes, not Nanite: `attr`, not `scope_tier` / `execution_pattern` (`TestNaniteSeedTriggersAgainstGoReflexesGrammar`). Do not add aliases for Nanite's two kinds.
- A misspelled predicate kind, at any depth, must be rejected (`TestMisspelledPredicateKindIsRejected`, conformance `misspelled-kind-*`). This is the reason the repo exists.
- Enums a host owns (`action_kind`, `status`, `provenance_tier`, event names) stay open: shape-checked, catalog only when injected (`TestWithKnownActionKindsAndTiers`, `TestWithKnownEvents`). Trigger kinds and predicate kinds are grammar and stay closed. Do not add a hard-coded status or action-kind list.
- `force_tool_choice`, `add_schedule`, `send_message` and unknown kinds get the object check only (`TestUnevidencedActionKindsAreObjectOnly`). Do not invent fields for them.
- `recurrence_override_seconds` is a pointer: nil inherits, `0` is an explicit no-cooldown, negative is invalid (`TestRecurrenceOverrideNilVersusZero`, `TestValidateDefinition`).
- Provenance-tier authorization is never checked here; only the tier's shape (`TestWithKnownActionKindsAndTiers`).
- Every seeded `action_spec` must stay accepted (`TestNaniteSeedActionSpecsAccepted`). The validator must not panic or hang on arbitrary input (`FuzzValidate`, `FuzzValidateDefinitionJSON`).
- Do not claim equivalence with Nanite's validation or go-reflexes' evaluator; the README's "Verified vs read" records what was run.
- Every invalid conformance case records its reason; `Load` rejects one that does not (`TestLoadRejectsMalformedCases`).
