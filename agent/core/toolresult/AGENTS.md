# go-toolresult

Bounded previews of oversized tool results with a scope-isolated fetch/search-by-id pointer cache.

It is not: a list paginator. Item lists, cursors and byte or token fitting of item slices belong to go-mcp's `budget` package; this module handles one serialized result that is too big.

## Start Here

- Root package `toolresult` — `preview.go`, `page.go` (`Select`, `ReadPage`, `SearchPage`) and `budget.go` are pure; `cache.go`, `store.go`, `handlers.go` and `spec.go` are the store-backed layers. `doc.go` is the package documentation.
- `memstore/`, `sqlstore/` — the two `Store`s; `storetest/` — the conformance suite both run.
- `examples/hello/main.go` — the runnable example; the README `## Usage` fence must stay identical to it.
- `testdata/golden/` — Nanite's own output for `golden_cases_test.go`; see its README.txt.
- `.github/workflows/check.yml` — the full CI gate; `release.yml` refuses a tag with no CHANGELOG heading.

## Commands

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
go test -run '^$' -fuzz '^FuzzPreview$' -fuzztime 30s .   # also FuzzReadPage, FuzzSelect, FuzzSearchPage
```

CI (`.github/workflows/check.yml`) is the full gate.

## Boundaries

- No `replace` directive in `go.mod` and no committed `go.work`: consumers cannot resolve either.
- Non-test dependencies are stdlib plus `github.com/oklog/ulid/v2` (`go list -deps ./...`). Do not import go-mcp, go-llm-types or go-sqlite; `modernc.org/sqlite` is test-only.
- Do not add list, cursor or envelope logic (the seam is written up in `doc.go`). If the budget brief and this one disagree, stop and ask.
- Scope is opaque and host-derived. Another scope's id and an absent id must be indistinguishable (`storetest` `CrossScopeAndAbsentAreIndistinguishable`, `CrossScopeReadIsNotFound`). Never accept a scope from tool `input`; the handlers take it as a separate argument (`HandlersUseSessionScopeOnly`).
- Errors and `Config.Exempt` tools are never cached (`ErrorsAndExemptToolsPassThroughUncached`); Present on a store error returns the unmodified body and no pointer (`StoreFailureReturnsBodyAndError`).
- The header, footer and tool-description text is prompt surface, byte-identical to Nanite's (`TestGoldenParityWithNanite`). Do not reword it without a decision; regenerate goldens only on purpose.
- Pages never split a rune and always make progress; consecutive pages tile the body (`TestReadPageTilesTheBodyExactly`, `FuzzReadPage`). The preview never exceeds its budget (`FuzzPreview`); the recovery notice is additional.
- `sqlstore` splices table and column names into SQL, so `New` and `DDL` panic on anything but a plain identifier (`TestInvalidIdentifiersPanic`). Values always use `?`.
- No logging, no background goroutines, no default purge; TTL and purge policy are the host's (blocked decision, config only).
