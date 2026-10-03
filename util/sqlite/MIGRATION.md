# Migration: go-sqlite -> libs/util/sqlite

`github.com/hollis-labs/go-sqlite` moved into the libs monorepo as `util/sqlite/`, with its full git history (18 commits, 2026-05-11 to 2026-09-30; source HEAD `2994d79ddafa`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-sqlite` | `github.com/hollis-labs/libs/util/sqlite` | `gosqlite` |
| `github.com/hollis-labs/go-sqlite/examples/inbox` | `github.com/hollis-labs/libs/util/sqlite/examples/inbox` |  (command) |
| `github.com/hollis-labs/go-sqlite/examples/serialwrite` | `github.com/hollis-labs/libs/util/sqlite/examples/serialwrite` |  (command) |
| `github.com/hollis-labs/go-sqlite/examples/single` | `github.com/hollis-labs/libs/util/sqlite/examples/single` |  (command) |
| `github.com/hollis-labs/go-sqlite/examples/split` | `github.com/hollis-labs/libs/util/sqlite/examples/split` |  (command) |
| `github.com/hollis-labs/go-sqlite/serialwrite` | `github.com/hollis-labs/libs/util/sqlite/serialwrite` | `serialwrite` |
| `github.com/hollis-labs/go-sqlite/sqlitekit` | `github.com/hollis-labs/libs/util/sqlite/sqlitekit` | `sqlitekit` |
| `github.com/hollis-labs/go-sqlite/txutil` | `github.com/hollis-labs/libs/util/sqlite/txutil` | `txutil` |

`go get github.com/hollis-labs/libs/util/sqlite@<version>` replaces `go get github.com/hollis-labs/go-sqlite@<version>`; the new module is `github.com/hollis-labs/libs/util`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/libs/util` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0) were not carried over; the first release of the new module will be tagged `util/v0.1.0` (not tagged yet).
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **API.** No symbol was renamed or changed by the move.
- **Package name.** The root package's clause is `gosqlite` while the directory is now `sqlite`, as it already differed from the repository name before. Imports of `github.com/hollis-labs/libs/util/sqlite` are referred to as `gosqlite` unless they carry an alias. The subpackages (`sqlitekit`, `serialwrite`, `txutil`) are unaffected.
- **Dependency versions raised.** One `go.mod` can require only one version of a dependency, so the highest one any imported lib asked for wins. For this lib that means:
  - `github.com/mattn/go-isatty`: v0.0.20 -> v0.0.24
  - `golang.org/x/sys`: v0.42.0 -> v0.48.0
  - `modernc.org/libc`: v1.70.0 -> v1.77.1
  - `modernc.org/memory`: v1.11.0 -> v1.12.1
  - `modernc.org/sqlite`: v1.48.1 -> v1.60.1
- **Files not carried to the new location** (git history still has them): `go.mod`, `go.sum`.
