# go-hitl

Kind-agnostic human-in-the-loop lifecycle: JSON Schema bundle, wire types, reference Service/Store, conformance suite.

It is not: an approvals product, a permission engine, a queue or inbox, or a place for any one application's presentation or policy. If a change needs a `surface`, a `queue_position`, a permit, a grant scope or a default-on-timeout action, it belongs in the caller.

## Start Here

- `README.md` and `docs/CONTRACT.md` — what the contract promises; `docs/CONFORMANCE.md` says what was and was not run.
- Root package `hitl` — wire types, `Store`/`Record`/`CheckSwap`, and the reference `Service`. Stdlib only.
- `schema/lifecycle.schema.json` — the bundle; regenerate with `scripts/genbundle.py` (needs Tangent's `request.schema.json`, read only). Fixtures and scenarios have `scripts/genfixtures.py` and `scripts/genscenarios.py`; edit the script, not the JSON.
- `hitltest/` — fixtures, scenarios, `RunConformance`, `RunStoreContract`.
- `examples/quickstart/main.go` — the runnable example; the README `## Usage` fence must stay identical to it.
- `.github/workflows/check.yml` — the full CI gate; `release.yml` refuses a tag with no CHANGELOG heading.

## Commands

```sh
export GOWORK=off
gofmt -l .
go vet ./...
go test -race -count=1 ./...
make tangent-check HITL_TANGENT_SCHEMA=/abs/path/to/request.schema.json   # opt-in
```

## Boundaries

- No `replace` directive in `go.mod` and no committed `go.work`: consumers cannot resolve either.
- Root `hitl` and `memstore` stay stdlib-only. The only external dependency is `santhosh-tekuri/jsonschema/v6`, imported by `schema` and `hitltest` alone. Never import go-envelopes, go-workflow or any application.
- Terminal outcomes are immutable and first terminal wins. Guards: `TestCanTransitionMatchesADR0001Exhaustively`, `RunStoreContract` (`TerminalRecordNeverChanges`, `ConcurrentSwapHasOneWinnerAndItsOutcomeSticks`), scenario `terminal-immutability`. Every terminal transition is a `Store.Swap` compare-and-set; do not add a second write path.
- A late reply is refused atomically even with no sweeper (D4). Guards: `TestRespondAfterExpiresAtIsRefusedBeforeAnySweeperHasRun`, `TestConcurrentRespondVersusExpiryHasOneWinnerAndNeverResolvesLate`. Expiry is decided from one clock reading per operation; do not re-read the clock between the check and the swap.
- Copied definitions (`COPY` in `scripts/genbundle.py`) must stay byte-equal to Tangent's; relax by adding a `...CoreV1` definition, never by editing a copy. Core definitions must not mention Tangent presentation fields.
- Do not decide the blocked questions: closed vs open `assurance` set, who runs a sweeper, quorum, escalation, delivery/acknowledgement. Do not name anything `Checkpoint*`.
- `proof{}` is carried, never verified, and never ranked against `assurance`.
