# toolmw

The adapter from the `core/util/mw` contract to go-mcp tool handlers.

It is not a second middleware contract. A change that gives tools their own descriptor, phase list or failure semantics is the wrong change; those are `mw`'s.

## Start Here

- `toolmw.go` — `Member`, `Stack`, `Wrap`, `Stack.Middleware`, the safe-error mapping and the standard set.

## Commands

```sh
export GOWORK=off
scripts/check -race mcp/plugin-mcp
```

## Boundaries

- Members are built once per tool, at registration. Guarded by `TestBuildRunsOncePerTool`.
- The adapter passes the shared scenario table. Guarded by `TestConformance`.
- A fault or recovered panic never reaches the client as its own text; it becomes a generic `internal` error. Guarded by `TestThroughRealServer`.
- go-mcp's own tool middleware keeps working through `Wrap`. Guarded by `TestThroughRealServer`.
- Argument sanitizing stays in the go-mcp `sanitize` package.
- Depends on `core/util` for `mw` (the mcp group may import core).
