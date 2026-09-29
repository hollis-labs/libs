# go-hooks

Hooks contract: 11-event vocabulary, allow/deny/ask decisions, per-hook failure mode, layer resolution; hosts implement the engine.

It is not an engine, a sandbox or a config loader. Hosts implement the engine; this repo is the contract, one subprocess helper and the conformance fixtures.

## Start Here

- `hooks` (module root) — pure types and functions: `event.go`, `input.go`, `output.go`, `hook.go`, `resolve.go`, `match.go`. Zero I/O.
- `cmdhook/` — the only code that starts a process. `conformance/` — the `go:embed` fixture tree (`testdata/<Event>/<case>/`) and the reference runner.
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
- Root imports neither `cmdhook` nor `conformance`, and does no I/O. Zero third-party dependencies and no hollis libs (`go list -deps ./...` is stdlib only).
- `OnError` has no default and never gets one, including inside `cmdhook.Runner`: `Hook.Validate` rejects "" (`TestHookValidate`, `TestOnErrorHasNoDefault`). `Runner` never reads it.
- Exit code 2 is a deliberate block, returned as a deny with a nil error; any other nonzero exit is an error (`TestRunExitTwoIsADenyNotAnError`, `TestRunOtherNonzeroExitIsAnErrorNotADecision`).
- `Hook.Timeout` is enforced even when the caller's context has no deadline (`TestRunEnforcesTimeoutWithoutCallerDeadline`).
- `Resolve` is whole-hook by name, pure and non-mutating (`TestResolveManagedWinsWholeHook`, `TestResolveDoesNotMutateInputs`). Do not turn it into a field merge without a decision.
- `Decision` values are pinned to go-permission's ("allow", "deny", "ask") with no import (`TestDecisionValuesPinned`). Native words like "block" belong in a host adapter.
- No `http` kind, no `Interrupt` or extension events, no `skill:`/`host:` prefix parsing.
- Fixture scripts must stay executable in git (`git ls-files -s conformance/testdata | grep -v 100755` should list only json); `TestScriptsAreExecutableOnDisk` guards it.
- Field names for the less common events were checked against the vendor docs on 2026-09-29; re-check before changing a struct tag.
