# Behavioral capability adapter checks

`capability/host/hosttest` runs capability probes through host adapters. Hosts
provide their published supported catalog and a test adapter that opens isolated
instances using the real dispatcher, policy ledger, credential issuer, backend
and audit sink:

```go
profile := hosttest.Profile{Supported: publishedHostDescriptors}
hosttest.RunProfile(t, applicationTestAdapter{}, profile)
```

`Profile.Supported` is a visible supported subset, never a per-probe waiver.
An explicit empty slice declares no capabilities and **fails** with
`no supported descriptor declared`; a host declaring neither shared nor
extension descriptors cannot obtain a passing report. A nonempty extension-only
catalog can pass. Host-wide raw admission, forged identity, lifecycle/commit and
secret-output probes still run. They use a declared supported descriptor, or a
baseline fixture when none is declared. Storage writes and other proposed
descriptors are optional for nonempty supported profiles. MCP and extension
probes run only when declared. Every absent shared descriptor receives an actual
raw call with a valid installed grant for that descriptor. It must return a
closed typed refusal with `not_started`, no retry
and zero effects. The code may be `unsupported_capability`, `capability_denied`,
`target_unavailable`, or another validated refusal except `internal_error`:
a grant-first SDK Enforcer
need not report the same code as a catalog-first host.
Both the returned report and `Report.String()` list these descriptors as
`declared_unsupported`. Extension-only profiles are supported; duplicate or
invalid profile names/definitions fail before calling the adapter. Native
descriptors receive scoped positive and bypass checks. Published shared descriptors must equal their canonical definitions,
including descriptions, operations and proposed status.

`Evaluate` returns results for each requirement and descriptor. `Check` is the
violation-only convenience view; it does not establish complete ADR coverage.
Check the intended requirement when proving a deliberately broken adapter fails:

```go
report := hosttest.Evaluate(ctx, brokenAdapter, profile)
for _, result := range report.Requirements {
    if result.ID == "C04" && result.Status != hosttest.Failed {
        t.Fatal("forged caller adapter passed caller verification")
    }
}
```

Results distinguish `passed`, `failed`, `declared_unsupported`, `not_run` and
`host_owned_not_covered`. ADR item 9 is always reported as host-owned and **not
covered**. `S09` is a separate extension-scope check, described below.

Adapters must submit `Attempt` to actual entry points. `Direct` bypasses SDK
client admission; `Bridge` selects the non-plugin MCP loopback path. Local
synthesized refusals cannot prove host enforcement. Preserve received application
error bytes in `Reply.Data`, the received frame in `Wire`, and measured transport
output in `WireBytes`.
A transport error fails the probe. Closed errors, observed code/effect state,
request correlation, retry safety and actual backend/budget observations are
checked independently. Suite-generated reasons are retained (for example,
`secret leaked to logs` and
`probe deadline exceeded`). Adapter error and panic text is withheld; only
validated closed error classifications are reported from adapter errors.

`Fixture` and typed `ChangeKind` controls are trusted test setup, never plugin
input. Provision canonical test resources, policy and bindings in isolated real
stores. Change ledger state, not future replies. Expiry advances grant time
without revoking a lease; restoring expiry must make it usable again. Revocation
removes authority independently. Tool revision/effect and callback graph changes
operate on the host's reviewed
definitions and binding ledger. The reference checks a reviewed definition map
for the actual server/tool and a graph rooted at the requested tool. These
controls test invalidation of a pinned definition and refusal of a configured
cycle. They do not prove how a production host builds its callback graph,
authenticates parent calls, or derives and propagates depth. `WidenBinding`
uses `Value` as `grantID/dimension`; `ToolRevision` and `ToolEffect` use the new
reviewed value. `Reconnect` authenticates a new plugin connection for the current
generation. `HostAvailable` restores service availability while keeping the
owner and its credentials live. Lifecycle controls cancel owned work; `HostUnavailable` changes
service availability without stopping the owner. Other controls need no value.

Instrument execution, activation, reservation acquisition/release, actual audit
delivery, plugin-received caller identity, proxy hops and actual fixture redirects with
`Observer`. Capture
actual logs, registry, browser, audit, reply and error artifacts. `SecretInput`
counts sensitive input at real output adaptation boundaries without retaining
it; probes require recorded inputs and scan raw, base64 and hex substrings of
at least eight bytes of the secret and issued credential. Base64 fragments are
checked at all three byte alignments, including within a larger encoded line;
arbitrary transformations, nested encodings and fragments shorter than eight
bytes are outside this scanner. Secrets are distinct
random fixture values. The reference sends configuration through Init and routes
it through pattern-based output adapters; it also drives a backend diagnostic
and scans received diagnostic reply bytes. Successful scoped replies are also
scanned for the issued bearer credential in their frame, data and payload.
Instrumentation is supplied by the host adapter:
counts alone cannot prove a real output path was exercised. Review that wiring
and the captured artifacts; counts or replies built from expected test outcomes
cannot establish host behavior.

`Gate` pauses actual work before or after commit. It acknowledges cancellation
but retains the worker until released, exposing premature reservation release.
`SecondGate` separates concurrent requests for cancel-call checks. A host must
serialize its final authority check with the actual commit boundary. Methods
must honor contexts; `Close` must release barriers and join fixture workers.
`Profile.Timeout` bounds each probe (one second by default), including cleanup.
A watchdog reports a violation and stops using an adapter that hangs; unfinished
requirements are `not_run` with a truncation reason and the run fails. No later
requirement is certified by that run. Go cannot terminate an uncooperative callback, so
its host adapter still owns cleanup. Suite invocation goroutines recover panics.
`RunProfile` derives the watchdog context from the calling test's deadline
when the reporter exposes `Deadline()`, reserving ten percent of the remaining
time (at most 100 milliseconds) for reporting before the test-binary timeout.
It logs truncation reasons as well as statuses.

| Result | Probed behavior |
| --- | --- |
| C01 | Missing/null/wrong grant contract refuses activation; empty grants deny; required unsupported requests fail by name; optional refusals produce named notices without losing unrelated authority. |
| C02 | Unknown capability/version/scope and wider bindings deny; planning rejects wrong versions and visibly narrows requests to policy; shared ownership cannot be overwritten. |
| C03 | Raw operations reject absent/revoked/expired/wrong-audience/wrong-owner/stale bindings and other targets; expiry and revocation have separate restoration controls; grants cannot borrow scope. |
| C04 | Forged caller claims in allowed calls cannot change delivered or audited identity; narrowed callers, forged session/agent dimensions, proxy credentials in plugin calls and old-generation replay fail; reconnect restores legitimate access. |
| C05 | MCP list/call admission, caller filtering, pinned definitions, effect, cycle/depth, byte/rate/concurrency limits; discovery after stop, expiry or revocation refuses. |
| C06 | Disable/stop/reload/disconnect cancel admitted work and refuse new work; running reservations remain held; definite commits survive withdrawal and ambiguous writes never retry. |
| C07 | Non-plugin MCP list/call/cancel; missing, garbage, plugin-binding and stale credentials refuse unauthenticated without effects or leaked reservations; origin and proxy probes, adapter-instrumented refusal to follow a fixture 302, fixture input and measured output limits, unavailable service and cancellation scoped to one concurrent request and actor, with credential reuse. |
| C08 | Unsafe installation cannot widen scope dimensions or activated grants; sensitive input instrumentation and captured output/received reply scans. |
| C09 | Host-owned workflow subsystem bindings: **not covered** by this package. |
| C10 | Scoped positive/raw bypass checks for published descriptors, canonical shared definitions and audit-independent decisions. |
| S09 | Exact supplied extension dimensions named provider/run/step/attempt/fork, effect, declared deadline/byte demand, grant expiry and revocation. These are scope-intersection checks, not ADR item 9. |

C07 exercises only the MCP bridge methods. Its redirect fixture sends a real
302 to a separate endpoint that records an execution if reached. The client
must report a refusal to follow with the observed 302, and the probe requires
one adapter-instrumented redirect response and zero target executions,
including a bounded settle re-check. Those counts certify the adapter
instrumentation supplied to the suite. A never-issued 302, an uninstrumented
target or a follow after the settle window can defeat that instrumentation;
review the real fixture/client wiring.
The reference normalizes this observed client-side refusal as `scope_denied`;
this is distinct from its server's received application-error data. The reference
measures the fixture input payload rather than the entire HTTP envelope. Its output limit uses actual
serialized fixture output, ignoring the client's `ResponseBytes` hint. A bounded
read may reject oversized output after reading with `not_committed`; it must not
emit the oversized data or retry. These probes do not supply a production
listener/client, a complete origin/redirect/address/proxy matrix, full streaming
or HTTP framing limits, or production authentication. Cancellation ownership is
probed in both directions between a plugin connection and a session-client
credential; hosts must
test their other actor and connection classes.
Hosts implement and test those boundaries through their real adapters.

S09 submits supplied scope dimensions against a fixture grant. It does not
verify identity derived from a workflow scheduler, issue run/step/attempt/fork
leases, verify fork or retry lineage, enforce a running-job wall-clock deadline,
or cancel workflow jobs. Hosts implement those ADR item 9 behaviors and provide
their own binding-ledger and lifecycle acceptance tests.

The reference tests use loopback HTTP servers, the shared Init decoder, a
credential store, live ledger snapshots, enforcement, mutable backend, budget,
plugin receiver and proxy receiver. Broken variants must fail their intended
requirement/probe. They help expose harness blind spots; their passing reference
counterpart does not certify a consumer, complete ADR coverage, stdio/duplex
profiles, MCP registration surfaces, browser isolation or an OS sandbox.
