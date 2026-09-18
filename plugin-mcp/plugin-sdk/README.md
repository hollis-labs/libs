# plugin-sdk

[![Go Reference](https://pkg.go.dev/badge/github.com/hollis-labs/plugin-sdk.svg)](https://pkg.go.dev/github.com/hollis-labs/plugin-sdk)

The plugin contract, in two halves. A universal **Go SDK** for building
plugins that talk to a host application over JSON-RPC stdio, and a
**TypeScript companion** (`ts/`) for the browser side — the registry a host
publishes and the loader that resolves it.

Both halves are host-neutral: neither has a dependency on any specific host
product, and host applications extend them with their own registration
surfaces in their own packages. They live in one repository so the registry
wire contract is defined once, with a Go view and a TypeScript view of the
same thing.

## Status

Pre-1.0 (`v0.x`). The wire protocol (`subprocess.ProtocolVersion = 1`)
and exported interfaces are stable in practice but the API may still
shift between minor versions; treat any minor bump as potentially
breaking and read the CHANGELOG before upgrading. Patch bumps
(`v0.x.y`) are documentation, examples, and internal hardening only.

## Install

```bash
go get github.com/hollis-labs/plugin-sdk
```

## What's in the box

- The `Plugin` contract and core base types (`Host`, `CRUDHandler`,
  `EventHook`, `UIComponent`, `Connector`, `ConfigFieldDef`).
- Typed plugin errors (`Error` / `PluginError`) with HTTP-friendly
  status codes, and the `ErrCancelled` sentinel for pre-hook
  cancellation.
- `EnvelopeOut` / `MessageOut` wire types for envelope emission.
- `subprocess` — JSON-RPC 2.0 wire protocol, `subprocess.Serve`
  entry point (handles stdin/stdout, dispatch, concurrency, panic
  recovery, signal-driven shutdown), capability interfaces
  (`CommandHandler`, `EventHandler`, `CRUDHandler`, `MCPHandler`,
  `HTTPHandler`, `Migrator`, `HealthChecker`), config / data /
  cache helpers, and a stderr JSON-lines logger with secret
  redaction.
- `subprocess/subprocesstest` — in-process test harness for driving
  plugins without spawning a real subprocess, with optional JSON
  roundtripping to catch wire-format bugs.
- `registry` — the Go view of the plugin registry wire contract: the
  response a host serves so a browser can find, load and resolve the UI
  its plugins ship, plus `Validate`.
- `ts/packages/plugin-registry` — `@hollis-labs/plugin-registry`, the
  browser half. The TypeScript view of the same contract, and a loader
  that dynamic-imports each plugin's ES module, resolves the named
  exports the registry names, and isolates load failures per plugin. The
  core entry point has no dependencies; `@hollis-labs/plugin-registry/react`
  adds the React adapter behind an optional peer.

## Quickstart

A minimum-viable plugin is roughly fifty lines:

```go
package main

import (
    "context"
    "os"

    "github.com/hollis-labs/plugin-sdk/subprocess"
)

type hello struct{}

func (hello) Init(ctx context.Context, p subprocess.InitParams) (subprocess.InitResult, error) {
    return subprocess.InitResult{
        ID:       "hello",
        Name:     "Hello",
        Version:  "0.1.0",
        Protocol: subprocess.ProtocolVersion,
    }, nil
}

func (hello) Load(ctx context.Context) (subprocess.LoadResult, error) {
    return subprocess.LoadResult{}, nil
}

func (hello) Unload(ctx context.Context) error { return nil }

func (hello) Command(ctx context.Context, req subprocess.CommandRequest) (subprocess.CommandResult, error) {
    return subprocess.CommandResult{Action: "message", Content: "hello, " + req.Args}, nil
}

func main() {
    if err := subprocess.Serve(hello{}); err != nil {
        os.Exit(1)
    }
}
```

A runnable copy lives at [`examples/hello/`](./examples/hello). Build
it with `go build -o hello ./examples/hello`; the
`examples/hello/hello_test.go` file demonstrates exercising the same
plugin in-process via the test harness.

## Layout

```
github.com/hollis-labs/plugin-sdk
├── doc.go                 package-level overview
├── plugin.go              Plugin, Host, CRUDHandler, EventHook, UIComponent, ...
├── errors.go              Error type, sentinels, constructors
├── envelope.go            EnvelopeOut, MessageOut
├── logger.go              Logger interface
├── docs/
│   ├── security-model.md  trust boundary, guarantees, and the seams
│   ├── best-practices.md  patterns, with worked examples
│   └── proposals/         open design proposals
├── examples/
│   └── hello/             minimum-viable subprocess plugin
└── subprocess/
    ├── protocol.go        JSON-RPC 2.0 wire types, methods, error codes
    ├── types.go           Init / Load / Command / Event / CRUD / MCP / HTTP / Migrate wire types
    ├── types_sdk.go       SDK-level Go types and capability interfaces
    ├── server.go          subprocess.Serve entry point
    ├── config.go          ConfigReader (with secret redaction integration)
    ├── data.go            DataHelper, CacheHelper
    ├── log.go             stderr JSON-lines logger
    └── subprocesstest/
        └── harness.go     in-process test harness
```

The browser half, an npm workspace nested one level down so Go tooling and
`node_modules/` stay out of each other's way:

```
ts/
├── package.json           private workspace root
└── packages/
    └── plugin-registry/   @hollis-labs/plugin-registry
        └── src/
            ├── types.ts        the wire contract, TypeScript view
            ├── loader.ts       createPluginRegistry
            ├── stylesheets.ts  the stylesheet sink and its default
            └── react.ts        the React adapter (optional peer)
```

## Security model and best practices

Two documents for anyone building against this SDK:

- **[`docs/security-model.md`](./docs/security-model.md)** — the trust boundary,
  what the SDK guarantees (panic isolation, credentials that do not travel in
  the environment, secret redaction in the logger, a pinned wire protocol), what
  it deliberately leaves to the host (authentication, sandboxing, resource
  limits, authorization, output filtering), and the seams where those guarantees
  stop.
- **[`docs/best-practices.md`](./docs/best-practices.md)** — patterns that hold
  up, with worked examples from hosts and plugins built on this SDK.

The one-line version, if you read nothing else: **whatever your plugin returns
is published** — to a terminal, a log, an HTTP response, or an AI model's
context window. Never return a vendor SDK type; map it onto your own, treat that
mapping as an allow-list, and test that a credential field cannot appear in the
output.

Open proposals live in [`docs/proposals/`](./docs/proposals).

## Versioning

Independent release cycle. Consumers pin a tagged version via `go.mod`.
See [CHANGELOG.md](./CHANGELOG.md) for per-release notes.

## Testing

```bash
go test ./...
```

The browser half builds and tests with `npm`, from `ts/`:

```bash
cd ts && npm install
npm run typecheck
npm test
```

For wire-format fidelity, enable JSON roundtripping in the harness so
every request and response is marshaled + unmarshaled before the
plugin sees it:

```bash
PLUGIN_SDK_JSON_ROUNDTRIP=1 go test ./...
```

The legacy `NANITE_PLUGIN_SDK_JSON_ROUNDTRIP` env var name is still
honored for backward compatibility but is deprecated; prefer
`PLUGIN_SDK_JSON_ROUNDTRIP` in new configurations.

## License

MIT — see [LICENSE](./LICENSE).
