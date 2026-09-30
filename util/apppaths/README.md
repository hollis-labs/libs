# go-apppaths

App path & workspace layout resolver for hollis-labs apps.

`go-apppaths` is the shared substrate that resolves where an application keeps
its files — the data, state, cache, and config roots, the main database path,
and the active workspace. Resolve once at startup, inject the result downward.
It is the path layer every per-app migration builds on.

Module path: `github.com/hollis-labs/go-apppaths`
Library package: `github.com/hollis-labs/go-apppaths/paths`

## Install

```sh
go get github.com/hollis-labs/go-apppaths/paths
```

## Usage

```go
import "github.com/hollis-labs/go-apppaths/paths"

layout, err := paths.Resolve("torque")
if err != nil {
    return err
}

layout.DataDir()   // ~/.local/share/torque
layout.StateDir()  // ~/.local/state/torque
layout.CacheDir()  // ~/.cache/torque
layout.ConfigDir() // ~/.config/torque
layout.MainDB()    // ~/.local/share/torque/workspaces/default/main.db
```

`Resolve` returns an immutable `Layout`. There is no global singleton and no
lazy resolution — resolve it once and pass it where it's needed.

### Behavior

- **XDG everywhere.** Base roots use the XDG layout on every OS
  (`~/.local/share`, `~/.local/state`, `~/.cache`, `~/.config`), honoring
  `$XDG_*_HOME` when set. The macOS/Windows defaults are deliberately
  bypassed so an app keeps one portable layout.
- **Precedence.** The main database resolves by: `WithDBOverride` >
  `<APP>_DB_PATH` env var > active workspace's database > the `default`
  workspace. The active workspace resolves by: `WithWorkspace` >
  `<APP>_WORKSPACE` env var > persisted pointer > `default`. The `<APP>`
  prefix is derived from the app name (`go-apppaths` → `GO_APPPATHS`).
- **Workspaces.** List, resolve, and select named workspaces; the active
  workspace is persisted as a small state-file pointer.
- **Project mode.** `WithProjectMode()` switches the roots to a CWD-local
  `./.<app>/` layout for self-contained runs.
- **Legacy adoption.** `WithLegacyNames(...)` migrates a prior app's
  directories on resolve — an idempotent move-if-target-absent that warns,
  and never clobbers, when both the old and new directory exist.
- **Owner-only permissions.** Every app-owned directory is `0700` and the
  active-workspace pointer is `0600` (`paths.DirMode`, `paths.FileMode`).
  See [Permissions](#permissions).
- **Materialization.** `Resolve` creates the resolved directories by
  default; `WithoutMaterialize()` opts out (e.g. for an `<app> path`
  introspection subcommand — see `layout.Describe()`).

`go-apppaths` resolves paths only. It never opens the database (compose it
with a separate sqlite layer) and never parses application config files.

## Permissions

Since v0.2.0 `Resolve` forces owner-only modes on everything the app owns:

| Path | Mode |
|---|---|
| data, state, cache and config roots | `0700` |
| workspace directory and the workspace database's directory | `0700` |
| `active_workspace` pointer file (written by `SelectWorkspace`) | `0600` |

The mode is set after creation, so it does not depend on the process umask and
it **retightens directories that already exist** at `0755` (what v0.1.x
created) on the next `Resolve`. There is no option to loosen it.

Deliberately left alone:

- **A database directory you named.** With `WithDBOverride` or `<APP>_DB_PATH`
  pointing outside the workspace, that directory is created if missing but its
  mode is never changed. Only the workspace's own database directory is forced
  to `0700`.
- **The shared XDG base directories** (`~/.local/share` and friends) that hold
  the app roots.
- **Symlinks.** A root that is a symlink is not followed for chmod, so nothing
  outside the app tree is modified.
- **Paths you may not chmod** (owned by someone else, read-only mount). They are
  left as found and `Resolve` still succeeds.
- **Windows**, where POSIX modes do not apply; the calls are no-ops there.

Run `go run ./examples/ownerperms` to see a `0755` root converge to `0700`.

## Layout

Importable shared-library layout — a package at the module root, no `cmd/`,
no `internal/`.

```
.
├── paths/                  # Library package — importable by other modules
├── examples/printpaths/    # Runnable example: resolve + Describe
├── examples/ownerperms/    # Runnable example: 0755 root retightened to 0700
├── go.mod
├── CHANGELOG.md
└── README.md (this file)
```

## Examples

```sh
go run ./examples/printpaths
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
