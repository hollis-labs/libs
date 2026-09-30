# go-svcerr

Small transport-agnostic typed error carrier: status, machine code, safe message, with errors.Is/As support.

It is not: TODO(author) — the mistake this repo attracts.

## Start Here

- `svcerr` package — the importable API; its `doc.go` is the package documentation.
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
- TODO(author): what a competent agent will get wrong here — invariants, the test that guards each by name, what must never happen.
