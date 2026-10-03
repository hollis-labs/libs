# Migration: go-otel -> libs/util/otel

`github.com/hollis-labs/go-otel` moved into the libs monorepo as `util/otel/`, with its full git history (29 commits, 2026-04-07 to 2026-09-30; source HEAD `5935b6145488`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-otel` | `github.com/hollis-labs/libs/util/otel` | `hotel` |
| `github.com/hollis-labs/go-otel/examples/hello` | `github.com/hollis-labs/libs/util/otel/examples/hello` |  (command) |
| `github.com/hollis-labs/go-otel/genai` | `github.com/hollis-labs/libs/util/otel/genai` | `genai` |
| `github.com/hollis-labs/go-otel/propagation` | `github.com/hollis-labs/libs/util/otel/propagation` | `propagation` |
| `github.com/hollis-labs/go-otel/redaction` | `github.com/hollis-labs/libs/util/otel/redaction` | `redaction` |

`go get github.com/hollis-labs/libs/util/otel@<version>` replaces `go get github.com/hollis-labs/go-otel@<version>`; the new module is `github.com/hollis-labs/libs/util`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/libs/util` module (one `go.mod` for all of its packages). Release tags of the old module (v0.0.1 v0.0.2 v0.1.0 v0.2.0 v0.3.0 v0.4.0 v0.5.0 v0.6.0 v0.6.1 v0.7.0 v0.8.0 v0.9.0 v0.10.0) were not carried over; the first release of the new module will be tagged `util/v0.1.0` (not tagged yet).
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **API.** No symbol was renamed or changed by the move.
- **Package name.** The root package's clause is `hotel` while the directory is now `otel`, exactly as the clause already differed from the repository name. Importers refer to it as `hotel` unless they carry an alias.
- **Dependency versions raised.** One `go.mod` can require only one version of a dependency, so the highest one any imported lib asked for wins. For this lib that means:
  - `golang.org/x/net`: v0.54.0 -> v0.58.0
  - `golang.org/x/sys`: v0.44.0 -> v0.48.0
  - `golang.org/x/text`: v0.37.0 -> v0.42.0
- **Files not carried to the new location** (git history still has them): `go.mod`, `go.sum`.
- **Telemetry scope names change.** The tracer and meter names (`otel` and `otel/genai`) are the packages' import paths, as before, so they are now `github.com/hollis-labs/libs/util/otel` and `github.com/hollis-labs/libs/util/otel/genai`. Exported spans and metrics carry the new `otel.scope.name`; dashboards, alerts or filters keyed on the old name must be updated.
