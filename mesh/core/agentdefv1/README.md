# go-agentdef

Parse, validate and fingerprint agent definition files: one markdown file per
agent, strict YAML frontmatter above the instructions. Layered sources, Agent
Skills resolution and hash-pinning, and an `agentdef` CLI for CI.

## Status

**Pre-release.** This project is unreleased, not deployed, and has no outside consumers. It's being built in the open: the code, the docs, and this README describe what exists today, not a pitch for what's planned. Interfaces and behavior change without notice, and there are no compatibility guarantees yet.

See [CHANGELOG.md](./CHANGELOG.md) for what exists and what changed.

## Install

```sh
go get github.com/hollis-labs/go-agentdef
```

The `agentdef` command has no tagged release yet; build it from a clone with
`go build -o agentdef ./cmd/agentdef`.

## Usage

```go
package main

import (
	"fmt"
	"log"

	agentdef "github.com/hollis-labs/go-agentdef"
)

const file = `---
name: incident-triage
description: Investigates and triages production incidents from an alert payload.
requires: [mcp]
---
You triage incidents. Start from the alert payload.
`

func main() {
	d, err := agentdef.Parse([]byte(file))
	if err != nil {
		log.Fatal(err)
	}
	if err = d.Validate(); err != nil {
		log.Fatal(err)
	}
	digest, err := agentdef.Digest(d)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(d.Name, digest)
}
```

The same program lives in [`examples/basic`](./examples/basic/main.go).

Two definitions with the same name in layers of equal precedence are an error,
not a silent winner. `LoadLayers` returns the collision so the caller must
handle it:

```go
_, err := agentdef.LoadLayers([]agentdef.Layer{
	{FS: os.DirFS("team-agents"), Name: "team"},
	{FS: os.DirFS("user-agents"), Name: "user"},
})
var ce *agentdef.CollisionError
if errors.As(err, &ce) {
	fmt.Println(ce.Name, ce.Layers) // e.g. triage [team user]
}
```

Give the layers different `Precedence` values and the higher one wins
(`Set.Collisions` still records it for audit). `ExampleLoadLayers` in
[`example_test.go`](./example_test.go) runs this for real.

### Frontmatter

`name` and `description` are required. Everything else is optional, and any
other key is an error.

| Field | Meaning |
|---|---|
| `name` | Identifier, `^[a-z0-9]+(-[a-z0-9]+)*$`. |
| `title` | Display name. |
| `description` | What the agent is and when to use it. |
| `identity` | Absent, or the literal `stable`. |
| `skills` | Agent Skills names, resolved to `skills/<name>/SKILL.md` beside the file. |
| `tools` | A request, never a grant. Not checked against any catalog. |
| `requires` / `uses` | Hard / soft capability names. |
| `hooks` | Hook names only. The host maps a name to an implementation. |
| `icon`, `avatar`, `tags`, `metadata` | Presentation and free-form string metadata. |

`Digest` hashes a canonical JSON form. List order is preserved, metadata keys
are sorted, the body is trimmed and LF-normalized, and the file's location is
ignored. Digests are `sha256:<hex>`; the format is provisional until the
launch-record digests it feeds settle on one.

`Validate` checks the capability names in `requires` and `uses` against a
catalog only when you pass `WithCapabilities(known)`; without it they are
pattern-checked only. The `agentdef` CLI passes the shared vocabulary from
[agent-contracts-leaf](https://github.com/hollis-labs/agent-contracts-leaf)
(`capabilities.Known`), so `agentdef validate` rejects a capability name outside
it.

### CLI

```sh
agentdef validate agents/triage.md          # strict parse, field rules, skill references
agentdef lint --strict agents/*.md          # duplicate entries, label-like descriptions, orphan skills
agentdef digest --skills agents/triage.md   # sha256 of the definition and of each skill tree
agentdef check agents/*.md                  # stale <!-- agentdef:generated --> spans
agentdef validate --layer root=agents,precedence=1 --layer root=local,precedence=2
```

Errors print as `<path>: <field>: <message>`. Exit codes: 0 ok, 1 a check
failed, 2 bad usage. There is no `--force`.

## Compatibility

This module is pre-1.0: minor releases may break the exported API, the
frontmatter schema, the canonical form and the digest string format, with no
promise of compatibility. There is no external consumer yet, so changes are
clean breaks: no aliases, shims or deprecation periods. Pin an exact version and
read [CHANGELOG.md](./CHANGELOG.md) before upgrading.

## Out of scope

- No provider, model, runtime or MCP configuration, and no launch, grant, trust or run-policy fields. Those belong to a host or a launch profile.
- No composition: no `extends`, no parts, no runtime merging or flattening of definitions. Shared text is generated into flat files at authoring time; this library only checks it has not gone stale.
- No effective-tools computation and no `--force`. `tools` is a request, and overriding an unmet `requires` is a launch-time decision for a host.
- No Tesseract calls, and no I/O beyond the `fs.FS` or directory a caller hands in.
- No git operations. A git checkout root is a directory the host has already checked out.
- No Agent Skills conformance validation beyond `name` and `description` presence in `SKILL.md`, which is what resolving and pinning a reference needs.
- No compatibility path for Nanite's `agent.Definition` (`slug`, `name` as display name, tolerant parsing) and no aliases to it.

## Development

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## License

MIT — see [LICENSE](./LICENSE).
