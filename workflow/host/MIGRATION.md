# Migration: go-workflow-host -> libs/workflow/host

`github.com/hollis-labs/go-workflow-host` moved into the libs monorepo as `workflow/host/`, with its full git history (13 commits, 2026-09-29 to 2026-09-30; source HEAD `0bafd7f56782`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-workflow-host/artifactfs` | `github.com/hollis-labs/libs/workflow/host/artifactfs` | `artifactfs` |
| `github.com/hollis-labs/go-workflow-host/sqlstore` | `github.com/hollis-labs/libs/workflow/host/sqlstore` | `sqlstore` |

`go get github.com/hollis-labs/libs/workflow/host@<version>` replaces `go get github.com/hollis-labs/go-workflow-host@<version>`; the new module is `github.com/hollis-labs/libs/workflow`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/libs/workflow` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0) were not carried over; the first release of the new module will be tagged `workflow/v0.1.0` (not tagged yet).
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **API.** No symbol was renamed or changed by the move.
- **Same module as the core.** `go-workflow-host` was a separate module that required `go-workflow`; it is now the `host/` directory of `github.com/hollis-labs/libs/workflow`, and that requirement became ordinary imports.
- **Layering is enforced by the core's import guard.** Core packages must not import `host/` (the guard now says so), and `host/` may use concrete drivers such as SQLite, like `adapters/`. The public API snapshot does not cover `host/`; it did not before either. Making host API part of the stability contract is an owner decision.
- **A consequence for the module's requirements.** The workflow module's `go.mod` now requires `modernc.org/sqlite` v1.58.0 and its indirect set because `host/` imports them. The core imports none of it, but anyone who depends on the module has those requirements in their module graph. Moving `host/` back into a module of its own would remove that; this move does not.
- **Dependency versions.** Everything this lib required is at the same version it had before.
- **Files not carried to the new location** (git history still has them): `.github`, `lefthook.yml`, `.folio.yaml`, `go.mod`, `go.sum`.
