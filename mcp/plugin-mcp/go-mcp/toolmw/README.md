# toolmw

Adapts the [`mw`](../../../../core/util/mw) middleware contract to go-mcp tool handlers: descriptor checks, phase ordering, failure modes, shadow mode and `Explain`, with each member built once per tool at registration.

## Status

**Pre-release.** This package is unreleased and has no outside consumers yet. Interfaces and behavior may change without notice, and there are no compatibility guarantees until the first tagged release of its module.

See the module's `CHANGELOG.md` for what exists and what changed.

## Install

```sh
go get github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/toolmw
```

## Usage

```go
stack, err := toolmw.Assemble(mw.Options{},
	toolmw.Recover(),
	toolmw.Wrap(mw.Descriptor{Name: "strict-args", Phase: mw.PhaseLimits, Failure: mw.Closed}, server.StrictArgs()),
	toolmw.DenyDestructive(),
)
srv := server.NewServer("example", "0.0.0", server.WithToolMiddleware(stack.Middleware()))
```

`stack.Explain(def)` prints what applies to one tool.

## Members

| Member | Phase | Failure | Notes |
|---|---|---|---|
| `Recover` | recover | closed | a panic below it becomes an error |
| `Audit(log)` | access-log | open | tool, duration and outcome; never arguments or results |
| `Timeout(d, perTool)` | limits | closed | per-tool overrides resolved at registration |
| `Authorize(check)` | authz | closed | the policy seam; return `Denied(msg)` for a safe refusal |
| `DenyDestructive` | authz | closed | refuses tools annotated destructive |
| `Wrap(desc, tm)` | yours | yours | adapts an existing go-mcp `ToolMiddleware` such as `StrictArgs` |

`Member.Applies` limits a member to some tools, for example everything not read-only.

## Errors

A denial is a `*budget.ToolError` (`permission_denied`). A middleware fault or a recovered panic reaches the client as a generic `internal` error, and a deadline as `timeout`; go-mcp reports any other error's text to the client, and a fault's text is the middleware's business.

## Limits

- Argument sanitizing is the go-mcp `sanitize` package's job and is not repeated here.
- The chain is validated once in `Assemble`; `Register` rebuilds the per-tool subset and panics only if that subset were invalid, which a valid chain cannot produce.
