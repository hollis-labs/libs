# plugin-sdk

Universal plugin SDK for the Nanite plugin architecture.

Provides:

- The `Plugin` interface and core plugin types (`Host`, `CRUDHandler`, `EventHook`, `UIComponent`, etc.)
- Typed plugin errors with HTTP-friendly status codes
- JSON-RPC 2.0 wire protocol for host/plugin communication over stdio
- A subprocess server library (`subprocess.Serve`) that handles stdin/stdout, RPC dispatch, concurrency, panic recovery, and shutdown
- A testing harness (`subprocess/subprocesstest`) for driving plugins without spawning a subprocess
- `EnvelopeOut` and `MessageOut` wire types for envelope emission

## Module layout

```
github.com/hollis-labs/plugin-sdk
├── plugin.go              Plugin, Host, CRUDHandler, EventHook, ...
├── errors.go              Error type, sentinels, constructors
├── envelope.go            EnvelopeOut, MessageOut
├── logger.go              Logger interface
└── subprocess/
    ├── protocol.go        JSON-RPC 2.0 wire types (RPCRequest/Response/Error, methods, codes)
    ├── types.go           Request/response types (InitParams, LoadResult, ...)
    ├── server.go          Serve(Plugin) entry point
    ├── config.go          ConfigReader
    ├── data.go            DataHelper, CacheHelper
    ├── log.go             stderr JSON-lines logger
    └── subprocesstest/
        └── harness.go     test harness
```

## Versioning

Independent release cycle. Nanite host and plugins consume via `go.mod` dependency on a tagged version.

Current status: v0.2.0 — yaml-authoritative load protocol (`LoadResult` ack-only), new method surface (`mcp/call_tool`, `http/handle`, `plugin/migrate`), and envelope propagation on command/event responses. **Breaking release** — see CHANGELOG.

## Development

Tests run with `NANITE_PLUGIN_SDK_JSON_ROUNDTRIP=1` to exercise wire-format roundtripping:

```bash
NANITE_PLUGIN_SDK_JSON_ROUNDTRIP=1 go test ./...
```

## License

MIT — see `LICENSE`.
