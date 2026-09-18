# go-materialize

Atomic, manifest-tracked file-tree materialization for Go: safe writes, symlink/traversal-safe staging, and ownership-scoped reconcile.

Module path: `github.com/hollis-labs/go-materialize`
Library package: `github.com/hollis-labs/go-materialize/materialize`

## Install

```sh
go get github.com/hollis-labs/go-materialize/materialize
```

## Usage

```go
import "github.com/hollis-labs/go-materialize/materialize"
```

## Layout

Scaffolded from folio's `go-lib` preset — an importable shared-library layout
(a package at the module root, no `cmd/`, no `internal/`).

```
.
├── materialize/   # Library package — importable by other modules
├── examples/                     # Runnable usage examples
├── go.mod
├── CHANGELOG.md
└── README.md (this file)
```

## Development

```sh
go test -race ./...   # tests
go vet ./...          # vet
gofmt -l .            # formatting check (no output = clean)
golangci-lint run     # lint
govulncheck ./...     # vulnerability scan
```

CI (`.github/workflows/check.yml`) runs the same checks on push and pull
request to `main`.

## License

MIT — see [LICENSE](./LICENSE).
