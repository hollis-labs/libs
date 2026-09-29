# Changelog

All notable changes to go-agentdef are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## Unreleased

### Added

- `Parse` / `ParseFile`: v1 agent definition files (YAML frontmatter plus markdown body), strict decode that rejects unknown keys, `name` and `description` required.
- `Definition.Validate` with `WithCapabilities`: name, identity and list-name rules; `requires`/`uses` checked against a caller-supplied catalog only when one is passed.
- `Canonical` and `Digest`: deterministic JSON form and `sha256:<hex>` digest that ignores source location, map order and body line endings.
- `LoadLayers`: merge directory roots with explicit precedence; equal-precedence name collisions are returned as `CollisionError`.
- `ResolveSkills` and `CopySkills`: find `skills/<name>/SKILL.md`, require `name` and `description`, and pin each skill by a hash over its whole tree.
- `Lint`, and `CheckGenerated` / `ParseGeneratedSpans` / `CheckSpans` for `<!-- agentdef:generated -->` spans (the marker syntax is provisional).
- `agentdef` command: `validate`, `lint`, `digest`, `check`, with repeatable `--layer`.

### Not yet done

- `WithCapabilities` is not wired to `agent-contracts-leaf`'s capabilities package; the option takes a plain function. No release is tagged until that dependency can be pinned without a `replace`.
