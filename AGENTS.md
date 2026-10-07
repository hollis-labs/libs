# libs

General-purpose Go libraries from Hollis Labs: `util`, `ui-go`, `workflow` and `plugin-mcp`. A multi-module repository: every top-level directory with a `go.mod` is a module with its own version and tags.

It is not: a place for agent, harness, LLM-provider or mesh code (that belongs in the `substrate` repository), a place for application code or deployment configuration, or a single Go module.

## Start Here

- `README.md` — what each module is for, the rules, how to develop across modules and how releases work.
- `<module>/` — one directory per module: `go.mod`, a package doc, a `CHANGELOG.md`. Packages arrive through `scripts/import-repo`. `plugin-mcp` contains the history-preserved plugin and MCP packages.
- `scripts/` — all tooling. `scripts/repo.conf` is the only per-repository setting; the other scripts are identical to the ones in the `substrate` repository.
- `.github/workflows/` — `<module>.yml` run one module (path-filtered) through `_module.yml`; `guards.yml` runs the repository-wide guards.

## Commands

```sh
export GOWORK=off                      # the scripts set it too
scripts/check [-race] [module...]      # gofmt, vet, build, test per module
scripts/check-layout                   # layout guard
scripts/check-one-way                  # dependency-direction guard
scripts/scan-public                    # secrets / hosts / private paths, tree and history
scripts/release [--apply] <module> <version>   # dry run by default; never pushes
scripts/import-repo [--target DIR] <source> <module>/<subdir>
```

## Boundaries

- Nothing here may reference `github.com/hollis-labs/substrate`: not in a `go.mod`, a `go.sum`, a `go.work`, or a Go import. Substrate may depend on libs, never the reverse. Guard: `scripts/check-one-way`, run by the `guards` workflow. If a lib seems to need something from substrate, the shared part moves down into libs.
- No `go.work` (git-ignored), no `replace` directive in a committed `go.mod`, no `go.mod` below `<module>/go.mod`, and `module` paths are `github.com/hollis-labs/libs/<dir>`. Guard: `scripts/check-layout`. For local cross-module work, replace temporarily and drop it before committing (see README).
- Modules are versioned independently. Tag only `<module>/vX.Y.Z` (for example `util/v0.1.0`). No bare `vX.Y.Z` tag, no repository-wide version, no change whose only purpose is to bump several modules together. Release in dependency order: dependency first, then the consumer's `go get`.
- `scripts/release` never pushes and no script pushes a tag. A pushed tag cannot be reused, so tag pushes are a maintainer's decision.
- This repository is public. Nothing secret, no token, internal host name, internal address or personal path may enter a file, a commit message or CI config. Run `scripts/scan-public` before pushing; history counts. Do not allowlist a finding to make it pass without saying why in the allowlist file.
- Bring code in with `scripts/import-repo`, not by copying files, so history survives. Old tags are not carried. After an import, merge the old `go.mod` requirements at their original versions into the module's `go.mod`; `go mod tidy` alone picks the latest.
- A module's stub package stays empty until real code arrives. Do not add placeholder APIs.
- The design kit and the plugin hooks live in their own repositories and stay there.
- Documentation names, in prose, only things a stranger can see in this repository or on the public Hollis Labs organization.

## Do Not

- Add a `go.work`, a `replace`, or a nested module to make something build.
- Add a `CLAUDE.md`; `AGENTS.md` is the single instruction file.
- Edit `scripts/` in one repository without making the same change in the other: they are meant to stay identical apart from `scripts/repo.conf`.
