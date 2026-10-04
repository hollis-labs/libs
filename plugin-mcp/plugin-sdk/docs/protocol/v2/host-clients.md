# Request-scoped host service clients

Go `subprocess.HostClientFromContext(ctx)` and TypeScript `ctx.host` expose an
SDK-owned client with seven helpers: storage get/put/delete, secrets get, egress
request, events publish, and log. Authors consume this client; they never
implement it. Later methods extend the same type.

Activation is currently private to conformance fixtures. Base and hooks-only
production connections have no host client. Implementing a hook handler does
not enable reverse RPC or give it a client. Negotiated reverse activation is a
separate slice. There is no public constructor, enable switch, or generic call.

Each call explicitly selects `grant_id` and supplies business arguments.
The SDK copies the validated Init grant snapshot and offered method ceilings;
it builds the required ReverseContext from the active request's host-owned
parent ID and binding reference. A cached client cannot acquire another
request's authority or start work after its owner terminates. Lifecycle scopes
permit only log, and hook handlers receive no client.

The call budget includes validation and serialization, and narrows to the
selected grant's `expires_at`, request deadline, offered method ceiling, and
caller budget. Go accepts a derived caller context; TypeScript accepts optional
`signal` and `timeoutMs`. There are no fallback method ceilings or invented
forward deadlines. Live binding expiry stays host-owned: it is not on the
ForwardContext wire. Only private fixtures may inject verified expiry metadata.
The host enforces binding and grant policy at admission and before commit.

The helpers use the existing bounded serializer, pending-call registration,
cancellable publication, and typed DTO validation. They never retry mutations,
issue HTTP requests locally, or bypass the host's policy. Storage mutations,
egress with operation keys, and events verify the correlated receipt's operation
key. A mismatched receipt fences correlation and reports `unknown_outcome`.
Validated host `-32010` failures remain typed (`HostRPCError` in Go,
`HostRPCFailure.data` in TypeScript), including host refusal detail. Transport
cancellation and deadline errors remain distinct from application veto; possible
mutation transmission retains `unknown_outcome` and `effect_state: unknown`.

Secrets return owned bytes plus host expiry metadata, with no cache. Before
exposure, their canonical base64 and exact decoded text forms register with the
connection's existing secret tracker. Host log redacts message, field names,
and nested JSON strings/keys before bounded serialization; oversized/deep field
redaction fails closed. This protects SDK logging, not arbitrary author stderr
or printf output.

The shared `host-storage`, `host-secrets`, `host-egress`, and `host-events-log`
fixtures run through Go and TypeScript request scopes and correlation engines.
Their Init offers and local metadata are fixture setup, not production
negotiation or proof of live host authorization.
