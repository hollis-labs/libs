# go-workflow

`go-workflow` is a deterministic, embeddable workflow engine for Go. It owns
the portable graph, compiler, execution-plan, runtime state-machine, wait,
typed-value, artifact, retry, fan-out, compensation, verification, memoization,
offline, schema, and conformance contracts. A consuming host owns durable
storage, scheduling, authentication, policy, transport, UI, and the concrete
step kinds it enables.

The repository intentionally has no root Go package. Import only the packages
your host needs, for example:

```go
import (
	"github.com/hollis-labs/go-workflow/compile"
	"github.com/hollis-labs/go-workflow/offline"
	"github.com/hollis-labs/go-workflow/stepkind"
)
```

Install an exact release:

```sh
go get github.com/hollis-labs/go-workflow@v0.1.0
```

Never resolve a workflow contract, schema, step kind, verifier, or plan by a
floating "latest" version. Pin the module version and freeze exact host
registries for each plan identity.

## Start here

- [Adoption guide](docs/workflow-engine-adoption.md) — minimal embedding path,
  host-owned seams, conformance levels, and production checklist.
- [Stability policy](STABILITY.md) — pre-v1 compatibility and versioning rules.
- [Dependency policy](DEPENDENCIES.md) — enforced core/import boundary.
- [Public API snapshot](public-api.txt) — exported declaration change guard.
- [Examples](examples/workflow/README.md) — graph-native source examples and their host
  capability assumptions.
- [Architecture decisions](docs/architecture/adr/) — the extracted engine
  boundary and core design decisions.

`runtime/inmemory` and the default `offline.Execute` path are useful for local
embedding, examples, and contract tests. They are not durable: they do not
survive process restart and must not be used to claim production recovery.

## Development gates

```sh
make check
```

That command verifies generation and module tidiness, then runs tests, the race
detector, vet, import/public-API guards, schema goldens, and the conformance
fixture manifest guard.

Copyright Hollis Labs. Licensed under the [MIT License](LICENSE).
