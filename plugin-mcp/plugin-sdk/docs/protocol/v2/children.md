# Real child replay

The test-only Node parent drives the same raw v2 base transcripts, internal
duplex recipe and lifecycle recipes against real Go, Node and Deno children.
`protocol/v2/fixtures/duplex-child.json` versions the selected profiles/cases and
lists the remaining owners. No reader, demux or SDK publication is mocked.

The minimum duplex recipe observes opposite-direction `id:1`, progress while
Init waits for a typed `host/log` reply, refusal of pipelined ordinary work,
reader progress during Unload's reverse log, once-only cleanup, terminal reply
and actual child exit. Its fixture installs internal directional correlation;
Init still declines reverse. This is engine evidence, not negotiated reverse
support or plugin-host interoperability. Expanded shared child cases exercise saturation, cancellation, deadlines,
writer capacity/fairness and effects. Hook composition stays explicitly
unavailable even with reverse active; a separate per-item scope/client design is required.

The parent also requests actual stdin EOF and SIGTERM during an awaiting
handler, then checks graceful exit, stdout exhaustion and unload effects.
Private fixture events carry observations; they never replace SDK stdout.
Go/Node control uses inherited fd3. Deno uses an atomically replaced file in a
private scratch directory, passed by environment, with read access only to
that directory. All three run a control feasibility case before replay.
Control EOF is joined during teardown so a blocked fixture read cannot retain
a Node child after the SDK has finished. No listener, protocol method or
production SDK flag enables this channel.

Parent limits are harness safety limits, independent of SDK deadlines:

| Resource | Ceiling |
| --- | ---: |
| Live child | 1, scenarios sequential |
| Stdout frame including LF/optional CR | 8 MiB |
| Unmatched frames | 32 and 8 MiB |
| Parent writes | 1 active write, 64 KiB chunks |
| Diagnostics retained / total stderr per case | 64 KiB / 1 MiB |
| Fixture event line / pending events | 4 KiB / 32 and 64 KiB |
| Scenario / individual progress watchdog | 20 seconds / 5 seconds |
| Failure SIGTERM grace / SIGKILL and reap watchdog | 1 second / 2 seconds |

These ceilings can only be narrowed. Parent raw fault recipes stream checked
padding/repetition rather than allocate the expanded oversized frame. Output
uses bounded byte framing and strict UTF-8, never readline or an unlimited
read-to-EOF. Failure closes parent-owned pipes, kills if needed and awaits the
single exit/close owner; killing is not a substitute for a successful graceful
exit. Harness self-tests cover hung/early children, oversized and truncated
stdout, stderr flood, blocked stdin, missing barriers and failed spawn.

Availability and obligation are separate. Both Go and TS validate file/step
levels, preferred notes for historical quirks, and finding/owner metadata
before skipping proposed cases. Implemented normative cases fail on an
assertion. Unavailable proposals report their exact
owner; they do not count as passed. Historical v1 files are untouched.

From the pinned source checkout, after building the TS workspace:

```sh
GOFLAGS=-p=2 go test -race -c ./subprocess -o "$TMPDIR/duplex-child"
node ts/packages/plugin-sdk/test/child-replay.js go "$TMPDIR/duplex-child"
node ts/packages/plugin-sdk/test/child-replay.js node
node ts/packages/plugin-sdk/test/child-replay.js deno
```

Normal `go test` neither builds a Node parent nor requires npm. The dedicated
CI/gate builds the Go fixture once and supplies its path. Node 22 and 24 jobs
run their real child; Deno 2.x runs through the Node parent with cached local
modules and no network permission. Reports contain runtime, case, level/mode,
pass or named unavailability and a bounded failure classification.

For plugin-host interop, pin the full SDK source commit/pseudo-version and
corpus version, build these test-only assets, and record the corresponding
plugin-host commit and runtime versions. The npm tarball does not ship test
recipes/workers. The plugin-host maintainers designate and report the real host adapter; its
first pin follows this driver slice. That adapter must exercise its real
Conn/spawn/cancel/dispose and typed backends with the same raw vectors. The
SDK fake host cannot certify authorization, bindings, host-global limits,
backend effects or durable operation-key receipts. Real Go/Node negotiated
child replay and pinned host interop must pass before reverse activation merges;
internal fixture bypass cannot satisfy that gate.

The dedicated child matrix targets POSIX systems (Linux in CI); it relies on inherited pipes and SIGTERM/SIGKILL. These harness controls are test-only.

Expanded cases use private callback activation of the merged request-scoped
HostClient helpers, with valid fixture-owned offers and grants. The fake typed
host validates request/result DTOs, maintains operation-key receipts keyed by
owner/method/target/operation_key, records generation inside the receipt, and proves no
SDK retry after a classified unknown outcome. Receipt replay after a changed
owner generation executes the fake mutation once. This table is harness
behavior, not evidence about production backend durability or authorization.

Controlled callback and physical-write barriers cover 16 ordinary handlers,
eight pending reverse calls, two lifecycle permits, overload without a callback,
retained permits and base ID reuse, parent descendants versus siblings, absent
forward deadlines, queue-inclusive reverse clipping, frame/byte admission, terminal
credit arithmetic and committed overflow. Both writer directions face sustained
control traffic and assert ordinary progress within four control frames. The
fake host uses the same bounded writer over the real parent pipe. Cleanup
failure/panic, hung callbacks/cleanup and disconnect observe actual child exit.
The 200 ms shutdown and short forward budgets in fault recipes are fixture-only
safety/test values; they never change SDK defaults.

## Negotiated normal-Serve children

The same replay command also drives `TestNegotiatedFixtureChild` and
`negotiated-worker.js` through the public Go `ServeWithOptions` and TypeScript
`serve` APIs. These children never create or activate correlation, construct
clients, or inject grants. The parent sends a real Init offer and host-issued
bindings; callbacks consume `HostClientFromContext(ctx)` or `ctx.host`.
Private scope/writer observations report limits and accounting without changing
them. Controlled author/output barriers retain the existing deterministic
saturation and queue probes.

`negotiated-replay.js` replays the shared negotiation matrix, raw invalid offers,
actual typed bound helpers, author mutation of the received Init, unoffered
methods, provisional Init logging with colliding directional IDs, log-only
Load/Unload, failed Init and cached-client revocation, late replies, EOF/SIGTERM,
accepted minima and subsequent input bounds, and the consumed Init high-water.
The expanded child recipes additionally run with runtime-delivered clients and
an acknowledged reverse profile. Base cancellation and reusable string IDs stay
in the declined-profile matrix. Results explicitly distinguish
`normal-serve-negotiated` from `internal-test-only`.

For focused negotiated replay after building the Go fixture binary and TS:

```sh
node ts/packages/plugin-sdk/test/negotiated-replay.js go "$GOTMPDIR/duplex-child"
node ts/packages/plugin-sdk/test/negotiated-replay.js node
node ts/packages/plugin-sdk/test/negotiated-replay.js deno
```

The normal child-replay entry point includes both matrices in existing CI legs.
All children share the bounded parent, private control channel and guaranteed
reaping. A proposal is reported as unavailable, never a pass. Hook helpers stay
unconditionally unavailable, owned by the reverse-profile and Team F hook-context
maintainers pending a separate per-item composition design.

These are SDK fake-host transport proofs. Real plugin-host authority, binding
ledgers, commit-time revocation, host-global admission, trusted depth and durable
receipts still require a pinned Team E integration run before activation merges.
