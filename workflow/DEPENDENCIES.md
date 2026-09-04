# Dependency policy

The engine core is every Go package except `adapters`. Its direct imports are
deliberately narrow so the reusable engine remains independent from product
hosts and transport implementations.

Allowed imports are:

- the Go standard library, including `testing`;
- other packages under `github.com/hollis-labs/go-workflow`, except `adapters`;
- `gopkg.in/yaml.v3` for workflow source parsing;
- `github.com/santhosh-tekuri/jsonschema/v6` for schema validation; and
- `github.com/expr-lang/expr` for the expression language selected by ADR 0007.

Direct core imports are limited to those adopted roots. The production
dependency-graph check separately permits `golang.org/x/text` as the active
transitive closure required by `github.com/santhosh-tekuri/jsonschema/v6`.
Core source files must not import `golang.org/x/text` directly; changing
that transitive-only allowance requires an intentional guard and policy update.

No third-party test helper is currently adopted. Adding one, or adding another
schema, expression, or extraction-safe primitive dependency, requires an
intentional update to this allowlist and its guard tests.

Core must not import Hadron or another host; app, daemon, transport, registry,
settings, or persistence packages; module adapters; concrete Wails, HTTP,
MCP, SQLite, model-provider, LLM, or agent SDKs; or sibling application
packages. Those dependencies belong in `adapters`, host
bindings, or the consuming application.

`go test ./internal/importguard` scans every core Go source file,
including tests and files selected only on other platforms. It also checks the
active build's production dependency graph so forbidden transitive packages do
not enter through an allowed import. Directories named `testdata` are ignored,
which keeps the deliberately forbidden test fixture out of normal builds. The
repository `make test` target includes all module packages and therefore runs
the guard.

The guard separately resolves the entire public module dependency
graph, including concrete adapters, and rejects every Hadron `internal/...`
dependency. It also compares exported declarations for every non-internal
package with [`public-api.txt`](public-api.txt). Refresh that snapshot
only after reviewing the compatibility policy in
[`STABILITY.md`](STABILITY.md); the
snapshot intentionally excludes documentation, source positions, function
bodies, and unexported struct fields.
