# go-materialize

Atomic, manifest-tracked file-tree materialization for Go: safe writes, symlink/traversal-safe staging, and ownership-scoped reconcile.

Module path: `github.com/hollis-labs/go-materialize`
Packages: `artifact` (provider-independent materialization inputs — `Entry`,
`Tree`, `Digest`, `Ownership`, `Provenance`) and `materialize` (the write
engine — `DefaultEngine.Apply`, `Reconcile`/`Refresh`, `MergeDocument`).

Extracted from `agentkit`'s `artifact`/`materialize` packages (agentkit
v0.6.1) so both agentkit and other tree-writing callers (e.g. folio) can
depend on one shared, zero-runtime-dependency core instead of duplicating
the write path. See each package's `doc.go` for the full contract.

## Install

```sh
go get github.com/hollis-labs/go-materialize
```

## Usage

```go
import (
    "github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
    "github.com/hollis-labs/substrate/harness/workspace/materialize"
)
```

## Layout

```
.
├── artifact/      # Entry/Tree value types, validation, content resolution
├── materialize/   # DefaultEngine: Create (atomic staged write) + Reconcile/Refresh
├── examples/       # Runnable usage examples
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
