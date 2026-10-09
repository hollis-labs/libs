# libs

General-purpose Go libraries from Hollis Labs: everything that is not specific
to agentic work. The agent harness, LLM core, mesh and agent runtime live in
[`substrate`](https://github.com/hollis-labs/substrate), which may depend on
these libraries. These libraries never depend on substrate.

## Status

`util`, `ui-go` and `workflow` hold code that was imported, with its git history,
from earlier standalone repositories. `plugin-mcp` holds the plugin SDK, plugin host and MCP libraries imported
from their standalone repositories. Releases are per module and tagged `<module>/vX.Y.Z`; the versions of
a module are listed in its own `CHANGELOG.md`.

## Modules

Each module has its own `go.mod`, its own version and its own tags.

| Module | Import path | Scope |
|---|---|---|
| `util` | `github.com/hollis-labs/libs/util` | Small, general-purpose utilities: SQLite helpers and backups, string helpers, service errors, transport parity, OpenTelemetry setup, SFTP sync, application paths, local-daemon helpers, a queue, a scheduler and a Tesseract memory API client. |
| `ui-go` | `github.com/hollis-labs/libs/ui-go` | Streaming and web UI building blocks for Go services: server-sent events, stream hubs, chat streaming, web UI helpers, directives and envelopes. |
| `workflow` | `github.com/hollis-labs/libs/workflow` | The workflow engine and its host. |
| `plugin-mcp` | `github.com/hollis-labs/libs/plugin-mcp` | Plugin SDK and host, MCP hosting/client/server helpers, sanitization and API projection. |

Use a module the usual way, once it has a release:

```sh
go get github.com/hollis-labs/libs/util@latest
```

## Rules

- **One-way dependencies.** Nothing here may reference
  `github.com/hollis-labs/substrate`, in a `go.mod`, a `go.sum` or an import.
  `scripts/check-one-way` enforces it, and CI runs it on every pull request.
- **Independent modules.** No `go.work`, no `replace` directive in a committed
  `go.mod`, no module nested inside another. `scripts/check-layout` enforces it.
- **Independent releases.** A module is released alone, with a tag that carries
  the module's directory as a prefix: `util/v0.1.0`, `workflow/v0.3.2`. There are
  no repository-wide versions and no lockstep releases.
- **Public from the first commit.** No secrets, tokens, internal host names,
  internal addresses or personal paths in files, history, CI or commit messages.
  `scripts/scan-public` checks all of them.
- **Separate repositories stay separate.** The design kit and the plugin hooks
  are not part of this repository.

## Developing across modules (there is no `go.work`)

A `go.work` makes code build on one machine and nowhere else, and it hides the
version a module really requires. This repository has none: it is git-ignored,
CI fails if one is committed, and every script and workflow runs with
`GOWORK=off`.

Work on one module at a time:

```sh
scripts/check util          # gofmt, go vet, go build, go test
scripts/check -race util    # the same, with the race detector
scripts/check               # every module
```

When a change spans two modules, land it in dependency order, one module at a
time. For example, when `workflow` needs something new in `util`:

1. Change `util`, merge it, and release it (`util/vX.Y.Z`, see below).
2. In `workflow`, run `go get github.com/hollis-labs/libs/util@vX.Y.Z`, then make
   the change that uses it.

To try an unreleased change before releasing it, point the consumer at your
local copy with a `replace`, and drop it again before you commit:

```sh
cd workflow
go mod edit -replace github.com/hollis-labs/libs/util=../util
# ... work, run scripts/check workflow ...
go mod edit -dropreplace github.com/hollis-labs/libs/util
```

`scripts/check-layout` fails on any committed `replace`, so a forgotten one is
caught before it reaches `main`.

## Releasing a module

```sh
scripts/release util v0.1.0           # dry run: validate, print the tag util/v0.1.0
scripts/release --apply util v0.1.0   # also create that annotated tag in your clone
```

The script checks that the version is valid semver, that it is newer than the
module's latest tag, that `util/CHANGELOG.md` has a section for it, that the
working tree is clean and that `main` is checked out. It never pushes. A
maintainer pushes the tag (`git push origin refs/tags/util/v0.1.0`), and a
pushed tag is permanent: the Go module proxy caches it.

## Tooling

The scripts are identical to the ones in `substrate`; only `scripts/repo.conf`
differs.

| Script | Purpose |
|---|---|
| `scripts/check [-race] [module...]` | gofmt, vet, build and test, per module. |
| `scripts/check-layout` | No `go.work`, no `replace`, one `go.mod` per module at `<module>/go.mod`, module paths that match their directory. |
| `scripts/check-one-way` | No reference to a forbidden module prefix (see `scripts/repo.conf`); here, substrate. |
| `scripts/release` | Validate a release and print its module-prefixed tag. |
| `scripts/import-repo` | Import an existing repository into a module subdirectory with its history. Needs [`git-filter-repo`](https://github.com/newren/git-filter-repo). |
| `scripts/scan-public` | Scan the working tree and the full history for secrets, internal hosts and addresses, and private paths. Uses `gitleaks` too when installed. |

CI runs one workflow per module, filtered to that module's paths, and a
`guards` workflow for the layout, the one-way rule and the public-hygiene scan.

## License

MIT. See [LICENSE](LICENSE). To contribute, read [CONTRIBUTING.md](CONTRIBUTING.md);
to report a vulnerability, read [SECURITY.md](SECURITY.md).
