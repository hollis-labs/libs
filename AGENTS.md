# libs

General-purpose Go libraries from Hollis Labs, in one public repository organised into groups: `core`, `security`, `mcp`, `ui`, `workflow`, `llm`, `mesh`, `harness`, `agent` and `app`. Every module lives at `<group>/<module>/` with its own `go.mod`, version and tags. A group directory is only a folder: it never carries a `go.mod`.

It is not: a place for application code or deployment configuration, or a single Go module.

## Start Here

- `README.md` — what each group and module is for, the rules, how to develop across modules and how releases work.
- `<group>/<module>/` — one directory per module: `go.mod`, a package doc, a `CHANGELOG.md`. Packages arrive through `scripts/import-repo`.
- `scripts/` — all tooling. `scripts/repo.conf` holds the groups, their dependency ranks and the module prefix.
- `.github/workflows/` — `modules.yml` plans the changed modules and runs each through `_module.yml`; `guards.yml` runs the repository-wide guards.

## Groups and dependency order

`core` < `security` < `mcp`, `ui`, `workflow`, `llm` < `mesh` < `harness` < `agent` < `app`

A module may import only modules in its own group or in a lower-ranked group. `mcp`, `ui`, `workflow` and `llm` have the same rank and are siblings: none of them may import another. Dependency cycles inside a group are rejected. If a lib seems to need something from a higher group, the shared part moves down.

## Commands

```sh
export GOWORK=off                                     # the scripts set it too
scripts/check [-race] [group | group/module ...]      # gofmt, vet, build, test
scripts/check-layout                                  # layout guard
scripts/check-one-way                                 # group dependency-direction guard
scripts/scan-public                                   # secrets / hosts / private paths, tree and history
scripts/release [--apply] <group>/<module> <version>  # dry run by default; never pushes
scripts/import-repo [--target DIR] [--module-root] <source> <group>/<module>[/<subdir>]
```

## Boundaries

- Module paths are `github.com/hollis-labs/libs/<group>/<module>`. No `go.work` (git-ignored), no `replace` directive in a committed `go.mod`, no `go.mod` in a group directory or below `<group>/<module>/go.mod`. Guard: `scripts/check-layout`. For local cross-module work, replace temporarily and drop it before committing (see README).
- Dependencies run one way, by group rank (above). Guard: `scripts/check-one-way`, run by the `guards` workflow.
- Modules are versioned independently. Tag only `<group>/<module>/vX.Y.Z` (for example `core/util/v0.1.0`). No bare `vX.Y.Z` tag, no repository-wide version, no change whose only purpose is to bump several modules together. Release in dependency order: dependency first, then the consumer's `go get`.
- `scripts/release` never pushes and no script pushes a tag. A pushed tag cannot be reused, so tag pushes are a maintainer's decision.
- This repository is public. Nothing secret, no token, internal host name, internal address or personal path may enter a file, a commit message or CI config. Run `scripts/scan-public` before pushing; history counts. Do not allowlist a finding to make it pass without saying why in the allowlist file (`scan-public.allow.d/<group>-<module>.allow`).
- Bring code in with `scripts/import-repo`, not by copying files, so history survives. Old tags are not carried. After an import, merge the old `go.mod` requirements at their original versions into the module's `go.mod`; `go mod tidy` alone picks the latest. Imports drop the source's per-module `AGENTS.md`, `CLAUDE.md`, `LICENSE` and `lefthook.yml`.
- Clean break: no shims, aliases, redirects, deprecation stubs or transitional re-exports for old module paths. An old path stops existing; consumers update their imports.
- A module's stub package stays empty until real code arrives. Do not add placeholder APIs.
- The design kit and the plugin hooks live in their own repositories and stay there.
- Documentation names, in prose, only things a stranger can see in this repository or on the public Hollis Labs organization.

## Do Not

- Add a `go.work`, a `replace`, or a nested module to make something build.
- Add a `CLAUDE.md`; `AGENTS.md` is the single instruction file.
- Put a `go.mod` in a group directory.
