# plugin-sdk

The plugin contract, in two halves, for plugins that talk to a host
application over JSON-RPC 2.0 stdio and ship UI into its browser.

The **Go module** owns the `Plugin` contract, the typed error and envelope
types, the wire protocol, the `Serve` loop and an in-process test harness. The
**TypeScript companion** under `ts/` owns the browser half: the registry a
host publishes and the loader that resolves it.

Both are deliberately not tied to any host product — hosts add their own
registration surfaces in their own packages. The Go module has no dependencies
outside the standard library, and the TypeScript core entry point has none at
all; React is an optional peer behind a subpath.

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
- `registry/registry.go` owns the registry wire contract's Go view and
  `Protocol`.
- `ts/packages/plugin-registry/src/types.ts` owns the same contract's
  TypeScript view; `loader.ts` owns `createPluginRegistry`, and `react.ts` the
  optional React adapter.

## Commands

```bash
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

The browser half, from `ts/` (run `npm install` there once):

```bash
npm run typecheck
npm test            # builds, then runs node --test against dist/
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

`registry.Protocol` and `PROTOCOL` in the TypeScript half are the same number
and are pinned on both sides. The two views are authored, not generated, and
there is deliberately **no shared golden fixture**: both halves of a fixture
are always at the same commit, so one can only catch drift introduced within a
single change, while the real risk is a host and a loader shipped at different
versions. Pinning the protocol on each side and round-tripping each side's own
types makes that mismatch something the loader reports at runtime — the same
answer `TestProtocolVersionLockedAt1` already gives for the subprocess wire.

The registry contract knows nothing about what a contribution *kind* means. A
kind is an open string and its metadata is opaque JSON, because one host's
envelopes, widgets and slots are another host's something else. Note that
`UIComponentType` in `plugin.go` is one host's taxonomy that already lives in
this module: the registry contract does not build on it, and unifying the two
would move a host's vocabulary into the shared contract. Also not here, and
for the same reason: any host's trust or isolation model, where bundles are
served from and under what CSP, and how a bundle obtains shared runtime
dependencies such as a React copy.

The loader derives its contribution table from the declared registry on every
sync rather than maintaining it incrementally. That is load bearing: wiring
contributions only when a bundle changed makes the contribution set a function
of the bundle rather than of the host manifest, and a manifest edit that does
not rebuild the bundle then never reaches the browser. The `regression:` tests
in `ts/packages/plugin-registry/test/loader.test.js` pin this and three related
refusals; each was verified to fail when the behaviour is reverted.
