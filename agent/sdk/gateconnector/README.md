# Gate connector (local integration slice)

`Connector` is opt-in per host integration point. Its zero value is disabled:
`Open`, `Reconcile` and `Cancel` return `ErrDisabled` before any store or GatePort
call. A host supplies a durable `Store` and a `gate.GatePort`; this package runs
no service, issues no identity, changes no policy, and executes no effects.

`Open` validates the exact prepared `gate.Request`, reserves its immutable
snapshot, then requests an interaction and attaches its ticket. A replacement
client calls `Reconcile(key, exactBinding)`; it loads the retained ticket and
queries Gatecrasher with `Await(MaxWait: 0)`. If an earlier client crashed before
ticket attachment, it retries the *same* immutable request and idempotency key.
The store must conflict on changed snapshots and tickets. A stale attempt is
rejected before a GatePort call. `Cancel` explicitly withdraws the interaction;
ending a wait does not cancel it.

`Project` yields reference-only evidence for transcript sidebands, invocation
records and tool cards. Decision, delivery and execution facts are separate.
`EncodeApprovalLine` turns that correlated evidence into an `approval` system
sideband for the published SDK transcript contract, with host-issued UUID,
session and timestamp. It preserves the native option/scope and separate
delivery and execution receipt refs; an unknown effect remains unknown. The
host must verify conversation/binding and append the line durably. Nothing in
this package writes a transcript or treats its line as an admission token.
It checks ticket/request/receipt correlation but is **not** authority verification
or admission. The executor must recheck host facts, call the authoritative
verifier, claim a valid grant atomically, and fence that claim through effect
start. A decision or a sent delivery does not prove execution.

The GatePort contract has no lookup by idempotency key. If the initial request
became terminal after a crash but before ticket attachment, and the port refuses
re-request after expiry, the host must use Gatecrasher's durable journal recovery
path. This connector cannot invent a missing ticket or recover from UI text.
The in-memory test store proves process replacement behavior only; it is not a
production durable store.

Gatecrasher service main at `ac24a5c855146625eff79064d7cbf287bcecaf1b`
exposes only response assessment and a bounded recipe route over HTTP. The
`testdata/gatecrasher-http-ac24a5c.json` snapshot pins those routes for the
contract test. `ServiceHTTPPort` returns `gate.ErrUnsupported` for capabilities,
Request, Await and Cancel; no client invents missing endpoints or treats response
assessment as an approval. Recheck the service contract before adding HTTP support.

Run `go test -race ./gateconnector` and `go test ./...` from `agent/sdk`.
