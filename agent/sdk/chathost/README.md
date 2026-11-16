# Durable chat host (experimental)

`chathost.Open(root, authorize)` provides the concrete `session.Conversations`
and `session.InputLeases` ports for SDK-2. The host supplies an `Authorize`
callback that authenticates every call; request IDs and owner labels are not
credentials. The package owns no provider process or service lifecycle.

Register an agent once with `RegisterAgent`. Its `Kind` is stable, task or
embedded; its single `Supervisor` is app, Nanite, Cerberus or Tether. Embedded
agents require app supervision. `session.Manager.Open` uses this host's `Create`
and `CompareAndSwap` to reserve and activate exact runtime attempts. The app
can instead `Load` a known conversation under an existing agent. `CreateChild`
starts a separate conversation with an explicit parent and empty history.

Consumers attach to the current exact binding with `Attach(binding, Viewer)`
or `Attach(binding, Controller)`. A viewer can replay committed events via
`ReadEvents(attachment, afterCursor)` but cannot acquire input. A controller
uses its attachment ID as `session.WriterID` with `Acquire`, then calls
`Submit` through the SDK session manager. One unexpired writer lease exists
per conversation. `Detach` removes a view and its lease, leaving history and
the runtime intact. `Publish` appends output from the host runtime. `Admit`
records event or approval references; admission alone never authorizes an
effect. Every input, output and message checks the complete active binding.

Committed cursors are sequential positions within one conversation. They are
not PTY byte offsets. Attachments and writer leases are invalidated when a new
generation reserves the conversation; clients reattach using the new binding.
All operations take an interprocess file lock, then atomically replace and sync
one JSON state snapshot. The directory must be on one local filesystem and
shared by all host processes that own this state. This format is suited to
small local hosts; large histories need a transactional storage adapter with
the same admission behavior. Native provider transcripts and Gatecrasher
decisions remain in their own authoritative stores.

The CHAT-2/3 integration seams are `Attach`, `ReadEvents`, `Acquire`, `Submit`,
`Detach`, `Publish` and `Admit`. Terminal control must obtain a controller
attachment and input lease; observers use viewer attachments. Status adapters
publish against the exact runtime generation. Frontends should preserve the
returned cursor for reconnect and show a gap if a future bounded adapter
cannot replay it.
