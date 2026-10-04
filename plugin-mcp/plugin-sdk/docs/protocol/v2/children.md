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
support or plugin-host interoperability. Expanded saturation/cancel/deadline/
effect and hook composition child cases remain with the next corpus slice;
the existing focused shared vectors continue to cover those engine behaviors.

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
assertion. The two deliberately unavailable proposals report their exact
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
child replay and pinned host interop must pass before reverse acknowledgement;
internal fixture bypass cannot satisfy that gate.

The dedicated child matrix targets POSIX systems (Linux in CI); it relies on inherited pipes and SIGTERM/SIGKILL. These harness controls are test-only.
