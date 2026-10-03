# Behavioral capability conformance

`capability/host/hosttest` exercises host entry points against the capability
contract. A host supplies an `Adapter`; each probe opens a fresh, isolated
`Instance` with a real policy ledger, connection binding, credential store,
backend and audit sink. The suite returns a violation for each failed probe.
It has no skip or waiver mechanism.

```go
func TestHostCapabilities(t *testing.T) {
    hosttest.Run(t, applicationTestAdapter{})
}
```

The adapter must send `Attempt` through the application's actual dispatcher or
HTTP bridge. Calling `Enforcer` directly in `Invoke`, constructing expected
replies in the adapter, or counting attempted requests as backend executions
cannot establish host conformance. `Direct` selects raw host operations without
SDK client admission. `Bridge` selects the non-plugin loopback client path.
Preserve actual application error data bytes in `Reply.Data`, including unknown
fields; the suite checks the closed error object before accepting a refusal.
Transport failures are failures of the probe, not evidence of authorization.

`Fixture` and `Change` are trusted test controls. Provision their canonical
resource names in an isolated application store, publish the selected supported
descriptors, and issue bindings and credentials through the real host issuer.
Map policy, caller, tool revision, lifecycle and expiry changes onto the real
ledger. A test control must change authority, not replace a future response.
`Attempt.Call`, `ClaimedCaller`, `EffectHint` and supplied usage are untrusted.
Determine operations, effects, actual I/O demand and verified callers at the
host boundary. `InitIdentity` is an identity courier and grants no authority.

Instrument actual execution and activation boundaries with `Observer`. Record
reservations when the budget acquires them and release when work finishes.
Record audit delivery at the sink and caller identity at the fixture plugin
receiver, independently of the host's audit metadata. Capture actual log,
registry and browser artifacts through `Artifacts`; return the issued fixture
credential through `Access` so the suite can detect its appearance in output.
Observers copy mutable metadata and support concurrent calls.

`Gate` pauses actual work before a commit or after a definite commit. A worker
calls `Gate.Wait` with its admitted context; it acknowledges cancellation but
keeps running until the probe releases it. This lets the suite detect premature
budget release, execution after withdrawal and loss of a definite write. The
adapter must serialize the final authority check with its commit boundary.
All methods must honor their contexts. `Close` must release barriers and join
fixture workers, even after failure. Probes have five-second contexts; the
calling test's timeout also bounds a broken adapter that ignores cancellation.

| Requirement | Executed behavior |
| --- | --- |
| C01 | Missing, null or wrong grant contract refuses activation; empty grants deny; a required unsupported request fails by name, while an optional request yields a named notice and preserves unrelated authority. |
| C02 | Unknown capability/version/scope and wider transport bindings deny; extension descriptors cannot overwrite shared ownership. |
| C03 | Raw operations deny missing, revoked, expired, wrong-audience, wrong-owner, stale-generation/host-epoch and out-of-scope bindings; one grant cannot borrow another grant's scope. |
| C04 | Forged user/session/agent identity and plugin use of proxy credentials deny; replaced generations are fenced; a verified caller reaches both the plugin and audit with the plugin actor retained. |
| C05 | MCP server/tool allowlists, effect ceiling, pinned revision, caller policy, cycles, depth, byte, rate and concurrency budgets apply at execution; discovery filters tools and a read hint cannot relabel a write. |
| C06 | Disable, stop, reload and disconnect cancel admitted work before commit and block new work; reservations remain held until workers finish; a definite commit survives late withdrawal and an ambiguous write never retries. |
| C07 | The loopback bridge rejects unrelated origins, redirects, proxies, excessive I/O and unavailable targets with typed errors; cancellation of one request leaves the credential usable for another. |
| C08 | Unsafe installation cannot widen grants; raw credentials and fixture secrets never appear in captured logs, registry or browser output. |
| C09 | Workflow calls bind provider/run/step/attempt/fork, effect, deadline and byte budget, and refuse expired/revoked grants. |
| C10 | Scoped positive and raw bypass calls run for each published descriptor, including native extensions; audit sink failure cannot change admission. |

C10 asks the host for its published inventory and probes every returned
descriptor in a fresh fixture. A published descriptor must have working
operations and its own scope schema. The baseline fixtures cover the shared
seed operations, storage, MCP and a workflow extension. Hosts can support
different deployment subsets, but this library suite does not waive behaviors
or silently skip fixture features. Run it against a test host configured for
the complete fixture contract; deployment-specific supported sets and negative
unsupported-name checks remain the host's responsibility.

The SDK's tests contain an HTTP dispatcher with the real shared Init decoder,
credential store, enforcement helper, budget and mutable backend. Separate
HTTP servers observe plugin identity delivery and attempted proxy hops.
Deliberately broken variants bypass admission, union grants, trust identity or
effect hints, replay bindings, leak errors/secrets, lose reservations, retry
ambiguous writes or publish unimplemented operations. The same `Check` suite
must reject each variant at its relevant requirement. Consumers should include
their own broken adapters to verify the suite reaches their enforcement seam:

```go
failures := hosttest.Check(ctx, applicationTestAdapter{disableScopeChecks: true})
if len(failures) == 0 {
    t.Fatal("broken admission adapter passed conformance")
}
```

A passing SDK reference adapter proves the harness exercises these behaviors.
A consumer claim requires running its own real adapter. This suite does not
certify stdio framing, duplex transport profiles, every MCP registration
surface, browser isolation, or an OS sandbox. Those boundaries have their own
transport, registry and application checks.
