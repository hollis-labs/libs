# Migration: go-workflow -> libs/workflow

`github.com/hollis-labs/go-workflow` moved into the libs monorepo as `workflow/`, with its full git history (65 commits, 2026-03-06 to 2026-09-30; source HEAD `9dd912c69285`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-workflow/adapters/agent` | `github.com/hollis-labs/libs/workflow/adapters/agent` | `agent` |
| `github.com/hollis-labs/go-workflow/adapters/call` | `github.com/hollis-labs/libs/workflow/adapters/call` | `call` |
| `github.com/hollis-labs/go-workflow/adapters/call/calltest` | `github.com/hollis-labs/libs/workflow/adapters/call/calltest` | `calltest` |
| `github.com/hollis-labs/go-workflow/adapters/checkpoint` | `github.com/hollis-labs/libs/workflow/adapters/checkpoint` | `checkpoint` |
| `github.com/hollis-labs/go-workflow/adapters/cmd` | `github.com/hollis-labs/libs/workflow/adapters/cmd` | `cmd` |
| `github.com/hollis-labs/go-workflow/adapters/emit` | `github.com/hollis-labs/libs/workflow/adapters/emit` | `emit` |
| `github.com/hollis-labs/go-workflow/adapters/gate` | `github.com/hollis-labs/libs/workflow/adapters/gate` | `gateadapter` |
| `github.com/hollis-labs/go-workflow/adapters/generatedapi` | `github.com/hollis-labs/libs/workflow/adapters/generatedapi` | `generatedapi` |
| `github.com/hollis-labs/go-workflow/adapters/generatedchild` | `github.com/hollis-labs/libs/workflow/adapters/generatedchild` | `generatedchild` |
| `github.com/hollis-labs/go-workflow/adapters/http` | `github.com/hollis-labs/libs/workflow/adapters/http` | `http` |
| `github.com/hollis-labs/go-workflow/adapters/llm` | `github.com/hollis-labs/libs/workflow/adapters/llm` | `llm` |
| `github.com/hollis-labs/go-workflow/adapters/mcp` | `github.com/hollis-labs/libs/workflow/adapters/mcp` | `mcp` |
| `github.com/hollis-labs/go-workflow/adapters/script` | `github.com/hollis-labs/libs/workflow/adapters/script` | `script` |
| `github.com/hollis-labs/go-workflow/adapters/service` | `github.com/hollis-labs/libs/workflow/adapters/service` | `service` |
| `github.com/hollis-labs/go-workflow/adapters/transform` | `github.com/hollis-labs/libs/workflow/adapters/transform` | `transform` |
| `github.com/hollis-labs/go-workflow/adapters/wait` | `github.com/hollis-labs/libs/workflow/adapters/wait` | `waitadapter` |
| `github.com/hollis-labs/go-workflow/authoring` | `github.com/hollis-labs/libs/workflow/authoring` | `authoring` |
| `github.com/hollis-labs/go-workflow/compile` | `github.com/hollis-labs/libs/workflow/compile` | `compile` |
| `github.com/hollis-labs/go-workflow/conformance` | `github.com/hollis-labs/libs/workflow/conformance` | `conformance` |
| `github.com/hollis-labs/go-workflow/diagnostic` | `github.com/hollis-labs/libs/workflow/diagnostic` | `diagnostic` |
| `github.com/hollis-labs/go-workflow/gate` | `github.com/hollis-labs/libs/workflow/gate` | `gate` |
| `github.com/hollis-labs/go-workflow/graph` | `github.com/hollis-labs/libs/workflow/graph` | `graph` |
| `github.com/hollis-labs/go-workflow/graph/schema` | `github.com/hollis-labs/libs/workflow/graph/schema` | `schema` |
| `github.com/hollis-labs/go-workflow/offline` | `github.com/hollis-labs/libs/workflow/offline` | `offline` |
| `github.com/hollis-labs/go-workflow/runtime` | `github.com/hollis-labs/libs/workflow/runtime` | `runtime` |
| `github.com/hollis-labs/go-workflow/runtime/inmemory` | `github.com/hollis-labs/libs/workflow/runtime/inmemory` | `inmemory` |
| `github.com/hollis-labs/go-workflow/runtime/runtimetest` | `github.com/hollis-labs/libs/workflow/runtime/runtimetest` | `runtimetest` |
| `github.com/hollis-labs/go-workflow/stepkind` | `github.com/hollis-labs/libs/workflow/stepkind` | `stepkind` |
| `github.com/hollis-labs/go-workflow/stepkind/stepkindtest` | `github.com/hollis-labs/libs/workflow/stepkind/stepkindtest` | `stepkindtest` |
| `github.com/hollis-labs/go-workflow/values` | `github.com/hollis-labs/libs/workflow/values` | `values` |
| `github.com/hollis-labs/go-workflow/verification` | `github.com/hollis-labs/libs/workflow/verification` | `verification` |
| `github.com/hollis-labs/go-workflow/wait` | `github.com/hollis-labs/libs/workflow/wait` | `wait` |

`go get github.com/hollis-labs/libs/workflow@<version>` replaces `go get github.com/hollis-labs/go-workflow@<version>`; the new module is `github.com/hollis-labs/libs/workflow`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/libs/workflow` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0) were not carried over; the first release of the new module will be tagged `workflow/v0.1.0` (not tagged yet).
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **API.** No symbol was renamed or changed by the move.
- **Module root.** The root of the old module is the root of the new one, so every package keeps its relative path: `github.com/hollis-labs/go-workflow/runtime` is now `github.com/hollis-labs/libs/workflow/runtime`. `go.mod` is the old one with its module line changed; its requirements are as they were, apart from the one listed below.
- **Hard-coded paths.** Places that spell the module path out were rewritten with the rest: the 32 lines of `public-api.txt` that name it, `workflowImportPath` in `internal/importguard`, and the package-path constants of the two generators (`compile/internal/planschema` and `graph/internal/schemagen`). No symbol was redesigned.
- **Nested module.** `test/external-consumer` has its own `go.mod`, which a module in this repository may not contain outside `testdata/`. It moved, with its history, to `testdata/external-consumer`; its `replace` still points at the module root and the `test-external` Makefile target uses the new path. It is not part of `go test ./...`, as before.
- **Package clauses** are unchanged: `adapters/gate` is package `gateadapter` and `adapters/wait` is package `waitadapter`.
- **Host.** `go-workflow-host` now lives in this module as `host/` (see `host/MIGRATION.md`). The import guard treats `host/` like `adapters/`, and the API snapshot (`public-api.txt`) does not cover it.
- **Dependency versions raised.** One `go.mod` can require only one version of a dependency, so the highest one any imported lib asked for wins. For this lib that means:
  - `github.com/google/pprof`: v0.0.0-20230207041349-798e818bf904 -> v0.0.0-20260802141513-ef3492d7dac3
- **Files not carried to the new location** (git history still has them): `.github`.
