# go-agentdef

Parser, validator, canonical digest and CLI for the v1 agent definition file (YAML frontmatter + markdown).

It is not a runtime, a launcher or a registry. It reads one file shape and says what it is; what a host does with it is not decided here.

## Start Here

- `agentdef` package — the importable API; its `doc.go` is the package documentation.
- `cmd/agentdef` — the CLI (`validate`, `lint`, `digest`, `check`); `run` is the testable entry point.
- `examples/basic/main.go` — the runnable example; the README first `## Usage` fence must stay identical to it.
- `definition.go` (types, `Parse`), `validate.go`, `lint.go`, `canonical.go`, `layers.go`, `skills.go`, `generated.go` — one concern per file.
- `.github/workflows/check.yml` — the full CI gate; `release.yml` refuses a tag with no CHANGELOG heading.

## Commands

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## Boundaries

- No `replace` directive in `go.mod` and no committed `go.work`: consumers cannot resolve either.
- Definition holds no provider, model, runtime, MCP, launch, grant or run-policy field, ever. Unknown frontmatter keys are a hard error (`TestParse_UnknownField_IsError`, which feeds Nanite's old tolerant fixture); do not loosen the decoder or add a legacy-key fallback.
- Clean break: no aliases, shims or `Deprecated` markers, and no interop with Nanite's `agent.Definition` (no `slug` fallback, no `name`/`title` alias).
- No composition (`extends`, parts, runtime merging) and no git library. A git checkout root is just a directory the host prepared.
- `tools` is a free-form request (`TestValidate_ToolsAreNeverCatalogChecked`); `hooks` has no registry (`TestValidate_WithCapabilities`). Only `requires`/`uses` may be checked against a catalog, and only through `WithCapabilities`.
- Canonical/Digest must not depend on `SourceRef`, `Layer`, map order or body line endings, and must depend on list order (`TestCanonicalDigest`). Changing the canonical form changes every digest downstream; that is a CHANGELOG entry.
- `LoadLayers` never silently picks a winner at equal precedence (`TestLoadLayers_EqualPrecedenceCollides`), and skips a layer's top-level `skills/` directory.
- Skill hashes cover the whole `skills/<name>/` tree (`TestResolveSkills_WholeTreeIsPinned`), not just `SKILL.md`.
- No `--force` in the CLI. `cmd/agentdef` tests drive `run(args, stdout, stderr)` against `testdata` fixtures; regenerate goldens with `AGENTDEF_UPDATE_GOLDEN=1` and read the diff.
- `WithCapabilities` takes a plain func, so the library imports nothing. Only `cmd/agentdef` binds it to `agent-contracts-leaf`'s `capabilities.Known`; the leaf is pinned at a tag (v0.1.0), never a `replace` and never a pseudo-version in a tagged release.
