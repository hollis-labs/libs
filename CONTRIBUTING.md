# Contributing

How a change gets from your clone into `main`. This page is deliberately short:
most of what you need is written closer to the thing it describes, and this
points at it rather than keeping a second copy that drifts.

## Before your first change

`README.md` explains the groups, the modules, the rules and how to work on one
module, or across two, without a `go.work`. `AGENTS.md` is the fastest
orientation to the layout and the boundaries that are not obvious from reading
the code.

You need Go (the version in the modules' `go.mod`), `git` and a POSIX shell. For
importing history you also need
[`git-filter-repo`](https://github.com/newren/git-filter-repo); `gitleaks` and
`shellcheck` are used when present.

## The sequence

1. **Branch.** `<type>/<short-slug>`, where the type matches the change: `feat`,
   `fix`, `docs`, `chore`. Nothing enforces it; it is what the history does.
2. **Change one thing, in one module where you can.** Modules are released
   independently, so a branch that touches two of them forces two releases and an
   order. If a change needs both, say so in the description and list the order.
   Respect the group order (`core` < `security` < `mcp`/`ui`/`workflow`/`llm` <
   `mesh` < `harness` < `agent` < `app`): a module never imports a higher group or a sibling group.
3. **Run the checks when the change is done**, not on every commit:
   `scripts/check <group>/<module>` (add `-race` for concurrent code),
   `scripts/check-layout`, `scripts/check-one-way` and
   `python3 scripts/scan-public --ref HEAD`.
4. **Push and open a pull request.** CI plans the touched modules
   (`modules.yml`) and runs the repository guards (`guards.yml`). Do not wait on
   CI to tell you what the scripts above already said.

Commit subjects use the conventional-commit shape: a type, an optional scope, a
colon, then the summary. To see what is actually in use:

```
git log --no-merges -40 --format='%s' | grep -oE '^[a-z]+(\([a-z-]+\))?:' | sort -u
```

## What a pull request should carry

The reviewer was not there when you made the decisions. State what the change
does, what it deliberately leaves alone, and the evidence that it works: the
commands you ran and what came back, not a claim that it passes.

If a number appears in the description, put the command that produced it beside
it. A figure nobody can re-derive costs the reviewer more than it saves.

## The one that cannot be undone

**A pushed tag.** The Go module proxy caches every version it serves, so a tag
that has been fetched once cannot be reused, moved or withdrawn, only superseded.
Tags are `<group>/<module>/vX.Y.Z` (for example `core/util/v0.1.0`) and only a
maintainer pushes them. `scripts/release` validates a release and prints the
tag; it never pushes. Write the module's `CHANGELOG.md` section before tagging.

The second thing that cannot be undone is **anything committed to a public
repository**. Secrets, internal host names, internal addresses and personal paths
stay in history even after a later commit removes them. Run
`scripts/scan-public` before you push, and do not add an allowlist entry to make
a finding pass without a stated reason.

## Things that surprise people

- **There is no `go.work`, and a committed `replace` is rejected.** Use a local
  `replace` while you experiment and drop it before committing (README,
  "Developing across modules").
- **Groups are folders, not modules.** A `go.mod` lives at
  `<group>/<module>/go.mod` and nowhere else.
- **Imports keep history.** Bring existing code in with `scripts/import-repo`
  (`--module-root` for a whole repository that becomes a module); do not copy
  files in. Old tags are not carried, and the imported `go.mod` requirements must
  be merged at their original versions.
- **Clean break.** No shims, aliases, redirects or deprecation stubs for old
  module paths; consumers update their imports.
- **The license file lives at the repository root only.** It covers every module.

## What this does not cover

- **Which change is worth making.** There is no roadmap here by design; that
  conversation happens in the issues.
- **Deployment.** These are libraries. How an application uses them is that
  application's business.
- **Reporting a vulnerability.** Do not use a public issue; see `SECURITY.md`.
