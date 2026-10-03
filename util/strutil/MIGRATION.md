# Migration: go-strutil -> libs/util/strutil

`github.com/hollis-labs/go-strutil` moved into the libs monorepo as `util/strutil/`, with its full git history (7 commits, 2026-04-09 to 2026-09-30; source HEAD `2862e3ab7f4d`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-strutil` | `github.com/hollis-labs/libs/util/strutil` | `strutil` |
| `github.com/hollis-labs/go-strutil/examples/case` | `github.com/hollis-labs/libs/util/strutil/examples/case` |  (command) |
| `github.com/hollis-labs/go-strutil/examples/inspect` | `github.com/hollis-labs/libs/util/strutil/examples/inspect` |  (command) |
| `github.com/hollis-labs/go-strutil/examples/manipulate` | `github.com/hollis-labs/libs/util/strutil/examples/manipulate` |  (command) |
| `github.com/hollis-labs/go-strutil/examples/random` | `github.com/hollis-labs/libs/util/strutil/examples/random` |  (command) |
| `github.com/hollis-labs/go-strutil/examples/slugify` | `github.com/hollis-labs/libs/util/strutil/examples/slugify` |  (command) |
| `github.com/hollis-labs/go-strutil/examples/truncate` | `github.com/hollis-labs/libs/util/strutil/examples/truncate` |  (command) |

`go get github.com/hollis-labs/libs/util/strutil@<version>` replaces `go get github.com/hollis-labs/go-strutil@<version>`; the new module is `github.com/hollis-labs/libs/util`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/libs/util` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0) were not carried over; the first release of the new module will be tagged `util/v0.1.0` (not tagged yet).
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **API.** No symbol was renamed or changed by the move.
- **Dependency versions raised.** One `go.mod` can require only one version of a dependency, so the highest one any imported lib asked for wins. For this lib that means:
  - `golang.org/x/text`: v0.36.0 -> v0.42.0
- **Files not carried to the new location** (git history still has them): `go.mod`, `go.sum`.
