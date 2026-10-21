# libs

General-purpose Go libraries from Hollis Labs, in one public repository
organised into groups. Every module lives at `<group>/<module>/` with its own
`go.mod`, its own version and its own tags. A group directory is only a folder:
it never carries a `go.mod`. The packages of the former `substrate` project are
being folded into the `agent`, `mesh`, `llm` and `security` groups.

## Groups

| Group | Purpose |
|---|---|
| `core` | Small, general-purpose utilities that everything else may build on. |
| `security` | Security building blocks: secrets handling and authentication, with policy planned. Present: `security/secrets`, `security/auth`, `security/policy` (the neutral decision contract) and `security/routepolicy` (its adapter for the `core/util/router` seam). |
| `mcp` | Plugin and Model Context Protocol libraries. |
| `ui` | Streaming and web UI building blocks for Go services. |
| `workflow` | Workflow engine and its host. |
| `llm` | LLM-facing libraries. No module is present yet. |
| `mesh` | Messaging and coordination between components. |
| `harness` | The layer that runs agent processes: launching, sandboxing, adapters, interception and brokering. Sits strictly below `agent`; imports `mesh`, `llm` and below. |
| `agent` | Agent runtime built on the harness: context, turns, tools, approvals and reflexes. |
| `app` | Reusable application assembly; consumers own product policy, identity and effects. |

### Dependency order

`core` < `security` < `mcp`, `ui`, `workflow`, `llm` < `mesh` < `harness` < `agent` < `app`

A module may import only modules of its own group (no cycles) or of a
lower-ranked group. `mcp`, `ui`, `workflow` and `llm` have the same rank and
are siblings: none of them may import another. If a lib seems to need
something from a higher group, the shared part moves down. The ranks live in
`scripts/repo.conf`; `scripts/check-one-way` enforces them.

## Modules

| Module | Import path | Scope |
|---|---|---|
| `core/util` | `github.com/hollis-labs/libs/core/util` | Small, general-purpose utilities: SQLite helpers and backups, string helpers, service errors, transport parity, OpenTelemetry setup, SFTP sync, application paths, local-daemon helpers, a queue, a scheduler, a worktree helper, a Tesseract memory API client, a middleware kit (HTTP, queue jobs, conformance suite), a rule router and a decision ledger. |
| `core/panics` | `github.com/hollis-labs/libs/core/panics` | Panic containment: recover at a boundary, return a ref and keep a redacted, bounded record. Goroutine, call and `net/http` guards; no dependencies. |
| `security/webauthn` | `github.com/hollis-labs/libs/security/webauthn` | UV-required WebAuthn primitives, durable single-use ceremonies, a credential-store interface, SQLite storage and synthetic authenticator tests. |
| `security/redact` | `github.com/hollis-labs/libs/security/redact` | Credential redaction for text, JSON, command lines, environments and logs: pattern rules plus exact known values, one marker, idempotent. Standard library only. |
| `security/pairing` | `github.com/hollis-labs/libs/security/pairing` | Device enrollment protocol and standalone SQLite mechanism: one-use grants, scoped credentials, expiry, renewal and revocation. See its README for host authorization and transport requirements. |
| `ui/ui-go` | `github.com/hollis-labs/libs/ui/ui-go` | Server-sent events, stream hubs, chat streaming, web UI helpers, directives and envelopes. |
| `mcp/plugin-mcp` | `github.com/hollis-labs/libs/mcp/plugin-mcp` | Plugin SDK and host, MCP hosting/client/server helpers, sanitization and API projection. |
| `workflow/engine` | `github.com/hollis-labs/libs/workflow/engine` | The workflow engine and its host. |
| `app/app-host` | `github.com/hollis-labs/libs/app/app-host` | HTTP/embedded SPA assembly, reviewed plugin registry delivery and process inventory. |
| `mesh/pipeline` | `github.com/hollis-labs/libs/mesh/pipeline` | Portable immutable message stages, bounded annotations and traces, and durable store interfaces. |

Subpackages sit below the module path, for example
`github.com/hollis-labs/libs/core/util/sqlite`. Each module's version history
is in its own `CHANGELOG.md`. Use a module once it has a release:

```sh
go get github.com/hollis-labs/libs/core/util@latest
```

Only the module paths above exist. Consumers import the `<group>/<module>`
paths; no shim, alias or redirect is provided for any earlier path.

## Rules

- **One-way dependencies.** Group rank decides what may import what (above).
  `scripts/check-one-way` enforces it and CI runs it on every pull request.
- **Independent modules.** No `go.work`, no `replace` directive in a committed
  `go.mod`, no `go.mod` in a group directory, no module nested inside another.
  `scripts/check-layout` enforces it.
- **Independent releases.** A module is released alone, with a tag
  `<group>/<module>/vX.Y.Z`. There is no repository-wide version and no
  lockstep release.
- **Public from the first commit.** No secrets, tokens, internal host names,
  internal addresses or personal paths in files, history, CI or commit
  messages. `scripts/scan-public` checks all of them.
- **Clean break.** No shims, aliases, redirects, deprecation stubs or
  transitional re-exports.
- **Separate repositories stay separate.** The design kit and the plugin hooks
  are not part of this repository.

## Developing across modules

A `go.work` makes code build on one machine and nowhere else, and it hides the
version a module really requires. This repository has none: it is git-ignored,
CI fails if one is committed, and every script and workflow runs with
`GOWORK=off`.

Work on one module at a time:

```sh
scripts/check core/util          # gofmt, go vet, go build, go test
scripts/check -race core/util    # the same, with the race detector
scripts/check mcp                # every module of a group
scripts/check                    # every module
```

When a change spans two modules, land it in dependency order, one module at a
time. For example, when `workflow/engine` needs something new in `core/util`:

1. Change `core/util`, merge it and release it (`core/util/vX.Y.Z`, see below).
2. In `workflow/engine`, run
   `go get github.com/hollis-labs/libs/core/util@vX.Y.Z`, then make the change
   that uses it.

To try an unreleased change first, point the consumer at your local copy with a
temporary `replace` and drop it again before you commit. A `replace` is never
committed:

```sh
cd workflow/engine
go mod edit -replace github.com/hollis-labs/libs/core/util=../../core/util
# ... work, run scripts/check workflow/engine ...
go mod edit -dropreplace github.com/hollis-labs/libs/core/util
```

`scripts/check-layout` fails on any committed `replace`, so a forgotten one is
caught before it reaches `main`.

## Releasing a module

Tags are `<group>/<module>/vX.Y.Z`, for example `core/util/v0.1.0`. Go's module
requirement uses only the `vX.Y.Z` part; the tag also carries the module
directory so each module versions on its own.

```sh
scripts/release core/util v0.1.0           # dry run: validate, print the tag core/util/v0.1.0
scripts/release --apply core/util v0.1.0   # also create that annotated tag in your clone
```

The script checks that the version is valid semver, that it is newer than the
module's latest tag, that `core/util/CHANGELOG.md` has a section for it, that
the working tree is clean and that `main` is checked out. It never pushes. A
maintainer pushes the tag (`git push origin refs/tags/core/util/v0.1.0`), and
a pushed tag is permanent: the Go module proxy caches it. Release in
dependency order: the dependency first, then the consumer's `go get`.

## Scripts

All tooling is in `scripts/`; `scripts/repo.conf` holds the groups, their
ranks and the module prefix.

| Script | Purpose |
|---|---|
| `scripts/check [-race] [group \| group/module ...]` | gofmt, vet, build and test, per module or per group. |
| `scripts/check-layout` | Groups hold modules; no `go.work`, no `replace`, no `go.mod` in a group directory, one `go.mod` per module at `<group>/<module>/go.mod`, module paths that match their directory. |
| `scripts/check-one-way` | Group dependency direction: no import of a higher-ranked group, no import between sibling groups, no cycle inside a group. |
| `scripts/scan-public [--repo DIR \| --source PATH-OR-URL] [--tree \| --history] [--ref REV ...]` | Scan the working tree and history for secrets, internal hosts and addresses, and private paths. `--ref REV` limits history to what is reachable from that revision (CI uses `--ref HEAD`). `--source` scans a repository about to be imported (clone, tip and full history) before it comes in. Uses `gitleaks` too when installed. |
| `scripts/import-repo [--target DIR] [--module-root] [--path-rename O:N] [--import-map O=N] <source> <group>/<module>[/<subdir>]` | Import an existing repository with its history. With `--module-root` the source becomes the module `<group>/<module>` itself; without it the source lands in a subdirectory of an existing module. Runs `scan-public --source` first. Needs [`git-filter-repo`](https://github.com/newren/git-filter-repo). |
| `scripts/release [--apply] <group>/<module> <version>` | Validate a release and print (or, with `--apply`, create locally) its `<group>/<module>/vX.Y.Z` tag. Never pushes. |
| `scripts/changed-modules [--base REV] [--all]` | Print the modules changed since `REV` (default `origin/main`) as JSON for the CI matrix; `--all` lists every module. |

Imports drop the source's own `AGENTS.md`, `CLAUDE.md`, `LICENSE` and
`lefthook.yml`, and do not carry old tags. After an import, merge the source's
`go.mod` requirements at their original versions into the module's `go.mod`;
`go mod tidy` alone picks the latest. Run `scripts/import-repo --help` for every
option.

## CI

- `modules.yml` runs `scripts/changed-modules` to plan which modules a change
  touches, then runs each through the reusable `_module.yml`: gofmt,
  `go mod tidy` is clean, build, vet and test.
- `guards.yml` runs the repository-wide guards: `scripts/check-layout`,
  `scripts/check-one-way`, `scripts/scan-public --ref HEAD`, and a parse check
  of the scripts.

## License

MIT. See [LICENSE](LICENSE). To contribute, read
[CONTRIBUTING.md](CONTRIBUTING.md); to report a vulnerability, read
[SECURITY.md](SECURITY.md).
