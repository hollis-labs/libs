# SDK hook bridge reference

This nested Go module and its private TS companion are test and reference code.
**Never tag, publish, or import this module in a production host.** A host copies
or reimplements the mapping. The parent plugin-hooks module remains stdlib-only;
plugin-sdk acquires no plugin-hooks dependency. No consumer adoption is included.

The Go module pins plugin-hooks `v0.1.0` and plugin-sdk
`v0.6.2-0.20261003235026-288a38aaa0c5` (SDK main `288a38a`, fixture PR #45).
The TS companion receives the actual SDK codecs from the same pinned source
snapshot; it has no npm SDK dependency or publication surface. Its remote types
are reference DTOs: the parent TS engine does not yet implement remote execution.

## Scope of the acceptance claim

> hookstest R16-R22 pass with zero waivers against real SDK children in Go and
> Node via the programmable test fixture; R19-R21 callbacks are host-side
> simulations (hooks_profile_version=1 WITHOUT plugin-originated callbacks).

**hooks_profile_version=1 WITHOUT plugin-originated callbacks (reverse lane not
exercised or claimed)** is the scope of this reference. It does not advertise
that profile in Init. Production SDK routing continues to decline hooks.
Plugin-originated callbacks need real-child reverse-lane tests after SDK duplex
and its author helpers land; this bridge does not exercise or claim them.

Fixture startup and Init occur when the host creates a remote scope, before
dispatch admission; process startup never consumes an admitted handler lease.
The acceptance adapter runs the whole published hookstest suite, without waivers
or renumbering. Local requirements retain local handlers. Every admitted remote
handler call sends the original forward request through the real child before
running its supplied probe closure on the host. The closure's outcome then
travels as JSON text in test-only `metadata.script` and returns through the SDK's
normal handler validation and reply codec. Batch calls use one ordered SDK batch
at each stage; notifications use a single notification and retain queued-only
accounting. The extra forward probe is test scaffolding, not a production retry.
Host closures own ancestry/cycle/depth/connection-fence simulation. They never
become plugin code, and no reverse RPC or host-closure IPC is introduced.

A correct SDK fails malformed author results closed to `failed/invalid_output`.
Named `raw:<name>` directives deliberately replace the serialized SDK reply to
exercise malformed host input. **Those replies are non-SDK fake-child behaviour**,
not evidence that an SDK author can emit malformed frames. These modes live only
in the SDK Go TEST binary and Node test child. The production enable seam remains
unexported/package-internal and absent from TS package exports.

## Mapping and authority

`Request`/`request` copy payloads and metadata, validate with SDK named codecs,
and put `{binding_id?, timeout_ms}` in forward context. Deadline, root/parent
invocation, depth, aggregate budget and trace stay at the top level. Go durations
round down to milliseconds; less than one millisecond or out-of-range leases
are refused, so a receiver never gains time from rounding.

The host supplies an incarnation from its own ledger. Opaque generation strings
are never parsed into wire integers. `RestoreRequest`/`restoreRequest` require the
original host snapshot and verify the wire against it. The original binding,
connection, opaque generation, monotonic deadline and ancestry remain host-owned;
wire diagnostics cannot grant authority. The core trace has no parent-span field,
so the forward mapping does not invent one.

`Result`/`result` validate closed DTO shape, request correlation and legal status
branches before mapping. Invalid replies become `failed/invalid_output`.
`cancelled` and `approval_required` are accepted only for bail actions; the Go
engine reconstructs its sentinels from that validated status, and TS
`resultError` uses the parent engine sentinels supplied to the bridge. Error text,
transport errors and JSON-RPC `-32003`/`-32010` never create a deliberate veto.
All 15 operational failure codes round trip through the SDK's closed validator.
Absent payload and explicit JSON null stay distinct. Named SDK codecs preserve
fraction/large-integer/escaped-key/HTML literals; a generic serializer can erase
that property and must not wrap a mapped payload on its way to the writer.

Batch item handles and dispatch outcomes remain engine-owned local objects.
The engine admits `RemoteBatchItem` handles before the bridge converts its
`RemoteRequest` list, and maps the returned result list to
`RemoteBatchOutcome` independently. The bridge sends observation actions only,
validates unique IDs, count and correlation, and never groups gates or filters.
The SDK processes batch items sequentially with leases starting at receipt;
`parallel` is a host catalog mode, not a promise of child parallelism.
Notifications contain no binding and no acknowledged result. A submission receipt
means queued, never handler success. Host admission, catalog/schema authorization,
breakers, unloading and transactional rollback remain the engine's responsibility.

## Checks and owned corpus

From this directory, with disk-backed `TMPDIR` set:

```sh
npm ci --prefix ts --ignore-scripts
./scripts/prepare.sh "$TMPDIR/hooks-sdk-env.sh"
. "$TMPDIR/hooks-sdk-env.sh"
go test ./...
npm run build --prefix ../ts
npm run typecheck --prefix ts
npm run build --prefix ts
npm test --prefix ts
```

Prepare fetches nothing during the tests: it creates a writable snapshot of the
pinned SDK module already downloaded by Go, builds its TEST binary, and installs
and builds its TS fixture dependencies ahead of the run. Tests require the
prepared source and fail if it is missing. No fixture/corpus is vendored here.
`protocol/v2/fixtures/hooks.json` is read from that snapshot for mapping checks;
the SDK's own real-child replay runners consume
`docs/protocol/v2/transcripts/hooks-handling.json` and `hooks-declined.json` there.
The same source feeds the TS codecs and real Node mapping tests. There is no
replace directive, workspace file, or committed machine path.

The final gate combines the parent Go and TS gates, nested module vet/race tests,
20 race repetitions of the real-child conformance adapter, and private TS
checks in one resource-limited invocation with `GOFLAGS=-p=2`.

## Latency limits

`TestLatency` reports 30 warm sequential samples for each child runtime, after
startup. Round trip includes the writer and response wait; end-to-end additionally
includes request and result mapping/codecs. Writer queue reports mutex acquisition
only under no contention. It excludes production scheduler queues, pipe
backpressure and network transport. Samples are observations on a shared machine,
not an SLO or a claim about production tail latency. See the PR's pasted gate
output for numbers from its final candidate.
