# Migration: go-queue -> libs/util/queue

`github.com/hollis-labs/go-queue` moved into the libs monorepo as `util/queue/`, with its full git history (23 commits, 2026-04-07 to 2026-09-30; source HEAD `f8d4f42e5007`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-queue` | `github.com/hollis-labs/libs/util/queue` | `queue` |
| `github.com/hollis-labs/go-queue/driver/memory` | `github.com/hollis-labs/libs/util/queue/driver/memory` | `memory` |
| `github.com/hollis-labs/go-queue/driver/noop` | `github.com/hollis-labs/libs/util/queue/driver/noop` | `noop` |
| `github.com/hollis-labs/go-queue/driver/sqlite` | `github.com/hollis-labs/libs/util/queue/driver/sqlite` | `sqlite` |
| `github.com/hollis-labs/go-queue/examples/inmemory` | `github.com/hollis-labs/libs/util/queue/examples/inmemory` |  (command) |

`go get github.com/hollis-labs/libs/util/queue@<version>` replaces `go get github.com/hollis-labs/go-queue@<version>`; the new module is `github.com/hollis-labs/libs/util`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/libs/util` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0 v0.2.0 v0.2.1) were not carried over; the first release of the new module will be tagged `util/v0.1.0` (not tagged yet).
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **API.** No symbol was renamed or changed by the move.
- **Dependency versions raised.** One `go.mod` can require only one version of a dependency, so the highest one any imported lib asked for wins. For this lib that means:
  - `github.com/mattn/go-isatty`: v0.0.20 -> v0.0.24
  - `golang.org/x/sys`: v0.42.0 -> v0.48.0
  - `modernc.org/libc`: v1.70.0 -> v1.77.1
  - `modernc.org/memory`: v1.11.0 -> v1.12.1
  - `modernc.org/sqlite`: v1.48.1 -> v1.60.1
- **Files not carried to the new location** (git history still has them): `go.mod`, `go.sum`.
