# plugin-sdk

A host-neutral Go SDK for building plugins that talk to a host application over
JSON-RPC 2.0 stdio. It owns the `Plugin` contract, the typed error and envelope
types, the wire protocol, the `Serve` loop and an in-process test harness. It
is deliberately not tied to any host product — hosts add their own registration
surfaces in their own packages — and it has no dependencies outside the
standard library.

## Start Here

- `README.md`'s "What's in the box" is the complete inventory.
- `plugin.go` declares the `Plugin` contract and core base types.
- `errors.go` owns typed errors and the `ErrCancelled` sentinel;
  `envelope.go` owns the envelope wire types.
- `subprocess/protocol.go` owns the JSON-RPC method names and
  `ProtocolVersion`.
- `subprocess/server.go` owns `Serve`: dispatch, concurrency, panic recovery
  and shutdown.
- `subprocess/log.go` owns the stderr JSON-lines logger and secret redaction.
- `subprocess/config.go` and `subprocess/data.go` own config, data and cache
  helpers.
- `subprocess/subprocesstest/` drives a plugin in-process, without spawning one.
- `examples/hello` is a complete minimal plugin.

## Commands

```bash
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

There is no CI workflow or Makefile in this repo.

## Boundaries

`subprocess.ProtocolVersion` is 1 and is pinned by
`TestProtocolVersionLockedAt1`. Hosts and plugins are separately versioned
binaries, so the wire protocol is a compatibility contract with things this
repo cannot see — `TestInitParamsForwardCompatV011` exists because an older
plugin must survive newer init params, and every message type has a roundtrip
test for the same reason.

A plugin panic must not take down the host conversation.
`Serve` recovers panics and returns a JSON-RPC error
(`TestServe_PanicRecovery`); removing that recovery converts a plugin bug into
a host crash.

The logger redacts values matching known secrets, fed from `config.Secret`
lookups through a secret tracker. Plugins log to stderr and hosts capture it,
so a logging path that bypasses `mergeKVs` writes credentials into host logs.

Keep the base contract host-neutral and dependency-free. The moment a host
product's types appear here, every other host inherits them.
