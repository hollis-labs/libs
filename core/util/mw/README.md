# mw

One middleware contract for every call surface: HTTP handlers, queue jobs, MCP tools, anything that takes a request and returns a response. A descriptor on each member is checked when the chain is assembled, not on every call.

## Status

**Pre-release.** This package is unreleased and has no outside consumers yet. Interfaces and behavior may change without notice, and there are no compatibility guarantees until the first tagged release of its module.

See the module's `CHANGELOG.md` for what exists and what changed.

## Install

```sh
go get github.com/hollis-labs/libs/core/util/mw
```

Standard library only (the HTTP adapter also uses `svcerr`, in the same module).

## Usage

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/hollis-labs/libs/core/util/mw"
)

func main() {
	stack, err := mw.Assemble(mw.Options{},
		mw.RecoverMember[string, string](),
		mw.TimeoutMember[string, string](time.Second), // each member brings its descriptor
	)
	if err != nil {
		panic(err)
	}
	h := stack.Wrap(func(context.Context, string) (string, error) { return "ok", nil })
	out, _ := h(context.Background(), "request")
	fmt.Println(out)
	fmt.Print(stack.Explain())
}
```

`stack.Explain()` prints the chain: order, phase, failure mode, mode.

## What assembly checks

| Check | Why |
|---|---|
| Phases are in order: recover, trace, access-log, limits, cors, authn, authz, rate-limit, identity, cache, outcome | Order mistakes (logging after auth, limits after the handler) are a defect class; catch them once, at startup |
| Failure mode is set on every member | There is no default. `open` carries on without the member; `closed` fails the call |
| Protecting phases (limits, authn, authz, rate-limit) are `closed`, observing phases (trace, access-log, identity, cache) are `open` | The other way round needs a written `Reason` |
| Mutating members are named in `Options.AllowMutating` | A member that rewrites the request must be opted into |
| `Requires` keys are `Provides`d by an outer member | A member that needs an identity must sit inside the one that sets it |

## Modes

Any member can run `Enforce`, `Shadow` or `Off`. `Shadow` runs the member against a stub next to learn what it would do, reports it through `Options.Observer`, applies nothing, and calls the real handler unchanged. The member's own side effects (counters, log lines) still happen, and a member that needs the real response sees a zero one.

## Adapters

| Package | Surface |
|---|---|
| `mw` | any `func(ctx, Req) (Resp, error)` |
| `mw/httpmw` | `net/http`; keeps the native `func(http.Handler) http.Handler` shape. Recover, request ID, access log, body limit, timeout, CORS |
| `mw/jobmw` | queue jobs. Rate limited, without overlapping, unique by key, throttle on exception, retry classification and backoff |
| `go-mcp/toolmw` (module `mcp/plugin-mcp`) | MCP tools, with go-mcp's types |

`mw/conformance` is the scenario table every adapter must pass: the same chain gives the same call order, observer events and outcome on each surface.

## Limits

- A failure mode applies to the middleware's own malfunction (a panic, or an error wrapped with `mw.NewFault`), not to its decisions.
- HTTP cannot change the outcome after the handler has written a response; a closed member that breaks after `next` re-panics for an outer recover.
- Store-backed members (`jobmw`) take their stores as interfaces. The in-memory ones are for tests and single-process use.
