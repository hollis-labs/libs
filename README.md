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

## Adopt the consolidated packages

A released module does not update an application's imports or selected build
graph. Apps can fail to build until their source, module requirements and API
callers are adopted together. Use published versions; do not repair a type split
with a committed `replace`, a `go.work` or a second copy of a contract.

The following releases are available as of 2026-10-09. Go's module requirement
uses `vX.Y.Z`; the repository tag also includes the module directory.

| Module requirement | Repository tag |
|---|---|
| `github.com/hollis-labs/libs/util@v0.2.1` | `util/v0.2.1` |
| `github.com/hollis-labs/libs/ui-go@v0.1.0` | `ui-go/v0.1.0` |
| `github.com/hollis-labs/libs/workflow@v0.1.0` | `workflow/v0.1.0` |
| `github.com/hollis-labs/libs/plugin-mcp@v0.1.1` | `plugin-mcp/v0.1.1` |

Use a supported toolchain that satisfies the selected modules' Go directives;
Go 1.26.9 was used for the current app adoptions. Read each selected release's
CHANGELOG before changing a pin.

### Old-to-new import map

Every path below begins with `github.com/hollis-labs/`. Retain subpackage
suffixes unless the linked migration note gives a more specific rule.

| Old import prefix | New import prefix |
|---|---|
| `go-apppaths/paths` | `libs/util/apppaths` (package `paths`) |
| `go-localdaemon` | `libs/util/localdaemon` |
| `go-otel` | `libs/util/otel` |
| `go-queue` | `libs/util/queue` |
| `go-scheduler` | `libs/util/scheduler` |
| `go-sftpsync` | `libs/util/sftpsync` |
| `go-sqlite` | `libs/util/sqlite` |
| `go-sqlite-backup` | `libs/util/sqlitebackup` |
| `go-strutil` | `libs/util/strutil` |
| `go-svcerr` | `libs/util/svcerr` |
| `go-tesseract-client` | `libs/util/tesseractclient` (package `tesseract`) |
| `go-transportparity` | `libs/util/transportparity` |
| `go-worktree` | `libs/util/worktree` |
| `go-chatstream` | `libs/ui-go/chatstream` |
| `go-directives` | `libs/ui-go/directives` |
| `go-envelopes` | `libs/ui-go/envelopes` |
| `go-ssekit` | `libs/ui-go/ssekit` |
| `go-streamhub` | `libs/ui-go/streamhub` |
| `go-webui` | `libs/ui-go/webui` |
| `go-workflow-host` | `libs/workflow/host` |
| `go-workflow` | `libs/workflow` |
| `plugin-sdk` | `libs/plugin-mcp/plugin-sdk` |
| `plugin-host` | `libs/plugin-mcp/plugin-host` |
| `mcp-host` | `libs/plugin-mcp/mcp-host` |
| `go-mcp-sanitize` | `libs/plugin-mcp/go-mcp-sanitize` |
| `go-mcp` | `libs/plugin-mcp/go-mcp` |
| `api-projection` | `libs/plugin-mcp/api-projection` |

Package clauses can differ from the last path component: keep existing aliases
and selectors. See the per-package `MIGRATION.md` files, the
[plugin-mcp package roots](plugin-mcp/README.md), and the
[substrate map](https://github.com/hollis-labs/substrate/blob/main/README.md#adopt-the-consolidated-packages)
for harness, llm-core, mesh and agent imports. The design kit and
[plugin-hooks](https://github.com/hollis-labs/plugin-hooks) remain separate.
`go-hooks` is a contract migration to plugin-hooks, not a prefix-only rewrite.

### Executable import-only codemod

Start in a clean, isolated consumer branch from freshly fetched `origin/main`.
Inventory every module, including nested application modules. The earlier
consumer-map and codemod rehearsal found that whole-module adoption avoids
old/new type-identity splits; its scratch `replace` directives and hypothetical
layouts are not consumer pins. The recipe below needs only Git and Go. It edits
Go import specs, preserves explicit aliases and uses longest whole-prefix
matches. It does not edit strings, comments, module requirements or API calls.

Create a temporary tool and a JSON map containing only the reviewed rules for
your consumer. This starter map covers SDK/host/webui and two common contract
moves; extend it from the tables and per-unit notes, not by inventing a blanket
`agentkit` rule.

```sh
scratch=$(mktemp -d)
cat > "$scratch/imports.json" <<'JSON'
{
  "github.com/hollis-labs/plugin-sdk": "github.com/hollis-labs/libs/plugin-mcp/plugin-sdk",
  "github.com/hollis-labs/plugin-host": "github.com/hollis-labs/libs/plugin-mcp/plugin-host",
  "github.com/hollis-labs/go-webui": "github.com/hollis-labs/libs/ui-go/webui",
  "github.com/hollis-labs/go-embed-contracts": "github.com/hollis-labs/substrate/llm-core/embedcontracts",
  "github.com/hollis-labs/agent-contracts-leaf": "github.com/hollis-labs/substrate/llm-core/contracts"
}
JSON
cat > "$scratch/rewrite.go" <<'GO'
package main

import (
    "bytes"
    "encoding/json"
    "flag"
    "fmt"
    "go/parser"
    "go/token"
    "io"
    "os"
    "sort"
    "strconv"
    "strings"
)

type edit struct { start, end int; text string }
type change struct { path string; data []byte; mode os.FileMode }

func run() error {
    mapPath := flag.String("map", "", "reviewed old-prefix to new-prefix JSON")
    write := flag.Bool("write", false, "apply; default prints a preview")
    flag.Parse()
    raw, err := os.ReadFile(*mapPath); if err != nil { return err }
    rules := map[string]string{}
    if err = json.Unmarshal(raw, &rules); err != nil { return err }
    keys := make([]string, 0, len(rules))
    for old, next := range rules {
        if old == "" || next == "" { return fmt.Errorf("empty prefix") }
        keys = append(keys, old)
    }
    sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
    files, err := io.ReadAll(os.Stdin); if err != nil { return err }
    changes := []change{}
    for _, name := range bytes.Split(files, []byte{0}) {
        if len(name) == 0 { continue }
        path := string(name)
        src, err := os.ReadFile(path); if err != nil { return err }
        fset := token.NewFileSet()
        file, err := parser.ParseFile(fset, path, src, parser.ImportsOnly)
        if err != nil { return err }
        edits := []edit{}
        for _, imp := range file.Imports {
            old, err := strconv.Unquote(imp.Path.Value); if err != nil { return err }
            for _, prefix := range keys {
                if old != prefix && !strings.HasPrefix(old, prefix+"/") { continue }
                next := rules[prefix]+strings.TrimPrefix(old, prefix)
                text := strconv.Quote(next)
                // The leaf root's package clause changed; keep its old qualifier.
                if imp.Name == nil && old == "github.com/hollis-labs/agent-contracts-leaf" {
                    text = "agentcontracts " + text
                }
                edits = append(edits, edit{fset.Position(imp.Path.Pos()).Offset,
                    fset.Position(imp.Path.End()).Offset, text})
                fmt.Printf("%s: %s -> %s\n", path, old, next)
                break
            }
        }
        if len(edits) == 0 { continue }
        sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
        for _, e := range edits {
            src = append(append(append([]byte{}, src[:e.start]...), []byte(e.text)...), src[e.end:]...)
        }
        info, err := os.Lstat(path); if err != nil { return err }
        if !info.Mode().IsRegular() { return fmt.Errorf("not a regular file: %s", path) }
        changes = append(changes, change{path, src, info.Mode().Perm()})
    }
    if *write {
        for _, c := range changes {
            if err := os.WriteFile(c.path, c.data, c.mode); err != nil { return err }
        }
    }
    return nil
}
func main() { if err := run(); err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) } }
GO
GOWORK=off go build -o "$scratch/rewrite-imports" "$scratch/rewrite.go"
# Review this list; exclude any deliberately frozen compatibility fixture.
git ls-files -z -- '*.go' ':!:vendor/*' ':!:third_party/*' > "$scratch/files"
"$scratch/rewrite-imports" -map "$scratch/imports.json" < "$scratch/files"
# After reviewing the preview, apply the same list and rules:
"$scratch/rewrite-imports" -write -map "$scratch/imports.json" < "$scratch/files"
git diff -- '*.go'
```

Review package qualifiers where current APIs renamed a package, especially
`plant`/`providerplant` to `planting`. Handle generated source, generators,
architecture guards, public API snapshots and whole-path string literals in a
separate owner-reviewed pass. A blanket replacement in fixtures or comments can
change the contract a test is supposed to exercise. For example,
`go-workflow-host` must never match a substring rule for `go-workflow`.

Then, **in each affected consumer module**, add only the releases it uses:

```sh
GOWORK=off go get github.com/hollis-labs/libs/plugin-mcp@v0.1.1 \
  github.com/hollis-labs/libs/ui-go@v0.1.0
GOWORK=off go mod tidy
git diff -- go.mod go.sum
git diff --check
```

Use `util@v0.2.1` or `workflow@v0.1.0` when those modules are needed. Inspect
remaining old requirements: `tidy` removes a direct dependency only after all
imports stop using it, and an unadopted upstream can still require it
transitively. Do not blindly drop a module that still supplies a live API.
Run focused changed-package checks during development, then the application's
required checks and CI before merging. This recipe does not authorize a
provider run, live deployment or data mutation.

### API and dependency-order notes

A prefix rewrite is the first step, not proof of behavioral compatibility.
Read the [harness and llm-core adoption notes](https://github.com/hollis-labs/substrate/blob/main/README.md#api-changes-that-need-application-work)
for explicit artifact custody, canonical rendering and embedding type identity.
For plugin/MCP consumers, `mcp-host`'s inprocess transport now uses the
plugin-host driver. It requires a genuine per-attempt owner `InitFactory`,
fresh incarnation, explicit roots/grants and verified Init agreement before
Load; a missing factory is not an implicit empty-authority choice. Follow the
[mcp-host quickstart](plugin-mcp/mcp-host/README.md), rather than adapting only
imports. For telemetry, use `InjectMCPMeta`/`ExtractMCPMeta` in `util/otel/propagation`
on the actual request `_meta`; retain the caller context on extraction and use
the returned metadata map. Do not restore deprecated arguments-level injection
or accidentally carry stale tracestate.

Dependency order matters across public types. [Chimera v0.1.0](https://github.com/hollis-labs/chimera/releases/tag/v0.1.0)
first adopted the consolidated registry; [Parallax PR #1](https://github.com/hollis-labs/parallax/pull/1)
then pinned it with plugin-mcp v0.1.1. Passing the new registry's `Response` to
an old Chimera build failed compilation even though the structs looked alike.
Likewise, use released Tesseract v0.11.0 or a later compatible release before
passing the new embedding/queue types through its public API. No local type
bridge is needed. These are source adoption outcomes, not deployment claims.

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
