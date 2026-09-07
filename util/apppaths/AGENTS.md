# go-apppaths

Resolves where an app keeps its files — data, state, cache and config roots,
the main database path, and the active workspace — into one immutable `Layout`.
It resolves paths and creates directories; it never opens the database and
never parses application config.

## Start Here

- `README.md` documents the precedence rules and every behavior flag.
- `paths/paths.go` owns `Resolve`; `paths/layout.go` owns the resolved
  `Layout` and `Describe`.
- `paths/options.go` holds the `With*` options that change resolution.
- `paths/workspace.go` owns workspace listing, selection and the persisted
  active-workspace pointer.
- `paths/adopt.go` owns legacy-directory adoption.
- `examples/printpaths/main.go` is a runnable resolve + describe.

## Commands

```bash
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI runs these. `golangci-lint run` is listed in the README's Development
section as well.

## Boundaries

XDG layout on every OS, on purpose. The macOS and Windows platform defaults are
deliberately bypassed so one app keeps one portable layout everywhere, and
`$XDG_*_HOME` is honored when set. Switching to platform-native directories
would look like a fix and would silently relocate every app's data —
`TestResolveDefaultRoots` and `TestResolveXDGOverride` pin the behavior.

Adoption never clobbers. Legacy present with the target absent moves; legacy
and target both present warns and leaves both untouched; legacy absent is a
silent no-op. It runs on every startup, so it has to stay idempotent —
`TestAdoptionConflictWarnsAndPreservesBoth` is the guard that matters.

There is no global singleton and no lazy resolution. Resolve once at startup
and inject the `Layout` downward; a second resolve under different environment
variables is a different answer, which is exactly the bug this shape prevents.

Precedence is part of the contract, not an implementation detail: the database
resolves `WithDBOverride` > `<APP>_DB_PATH` > active workspace > `default`, and
the workspace resolves `WithWorkspace` > `<APP>_WORKSPACE` > persisted pointer
> `default`.
