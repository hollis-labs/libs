# go-transportparity

Test helpers for asserting that HTTP, MCP and ACP doors fronting one operation agree on outcome.

## Status

**Pre-release.** This project is unreleased, not deployed, and has no outside consumers. It's being built in the open: the code, the docs, and this README describe what exists today, not a pitch for what's planned. Interfaces and behavior change without notice, and there are no compatibility guarantees yet.

See [CHANGELOG.md](./CHANGELOG.md) for what exists and what changed.

## Install

```sh
go get github.com/hollis-labs/go-transportparity
```

This is a test-helper library: import it from `_test.go` files, not from production code. It depends on [go-svcerr](https://github.com/hollis-labs/go-svcerr) to derive an error's category.

## Usage

The case that motivates it: one human-gate decision behind two doors. The MCP door checks the caller's scope; the HTTP door forgot to. Comparing response bytes would never notice, because each door is only ever tested with the right scope.

```go
package main

import (
	"fmt"

	svcerr "github.com/hollis-labs/go-svcerr"
	tp "github.com/hollis-labs/go-transportparity"
)

// printT lets this program show what a failing assertion would say; in a real
// test you pass *testing.T.
type printT struct{}

func (printT) Helper()                   {}
func (printT) Errorf(f string, a ...any) { fmt.Println("FAIL:", fmt.Sprintf(f, a...)) }
func (printT) Fatalf(f string, a ...any) { fmt.Println("FATAL:", fmt.Sprintf(f, a...)) }

func main() {
	httpDoor := func(scope string) tp.Outcome { // no scope check
		return tp.FromValue(map[string]any{"decided": true})
	}
	mcpDoor := func(scope string) tp.Outcome {
		if scope != "human_gate:write" {
			return tp.FromError(svcerr.New(svcerr.CodePermission, "scope missing"), svcerr.CodeInternal, 403)
		}
		return tp.FromValue(map[string]any{"decided": true})
	}

	tp.AssertSameOutcome(printT{}, "decide with the right scope", httpDoor("human_gate:write"), mcpDoor("human_gate:write"))
	tp.AssertSameOutcome(printT{}, "decide without the scope", httpDoor(""), mcpDoor(""))
}
```

## How to use it

You run each door yourself, with your own closure, and reduce each result to an `Outcome`: `FromValue(v)` for a success, `FromError(err, fallbackCategory, fallbackStatus)` for a failure (the category comes from a `*svcerr.Error` in the chain when there is one). Then compare:

- `AssertSameOutcome(t, name, a, b)`: both succeed or both fail; on failure the same **category**; on success the same value after `CanonicalJSON`. Status numbers and error wording are never compared (an HTTP status and a JSON-RPC code will not be equal). A failure with no category is rejected, because two uncategorized errors agree on nothing.
- `CanonicalJSON(t, v)`: marshal, decode, and re-marshal with sorted keys and normalized numbers, so map versus struct, pointer versus value, and `1` versus `1.0` do not cause false failures.
- `HTTPJSON(t, handler, method, path, body)`: call an `http.Handler` directly (no listener) and decode the JSON body.
- `AcceptedFieldNames(v)` and `AssertSameFieldNames(t, labelA, labelB, a, b)`: compare what two request structs ACCEPT, by JSON field path. Two doors can return identical bytes for the same fixture while one calls a field `key` and the other `memory_key`; this catches that.

A table of named doors, one row per operation, is the recommended calling convention; the package adds no type for it.

## Compatibility

This module is pre-1.0: minor releases may break the exported API. Pin an exact
version, and read [CHANGELOG.md](./CHANGELOG.md) before upgrading; every
breaking change is listed there.

## Out of scope

- A surface catalog with existence matching and waivers (which operations exist on which door). Tesseract's own suite of that shape is mostly per-app data; it does not generalize.
- A generic MCP or ACP invocation helper. Every app's call convention differs; you always supply the closure.
- Argument sanitization or validation middleware, and pagination or budget shape (go-mcp's job).
- Any decision about an app's error envelope (go-svcerr's).
- Being a production dependency: it is for tests.

## Development

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## License

MIT — see [LICENSE](./LICENSE).
