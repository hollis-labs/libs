# Changelog

All notable changes to `go-tether-client` are documented here. The format is
loosely based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
this project follows [Semantic Versioning](https://semver.org/). While the
major version is `0.x`, the API is considered pre-1.0 and breaking changes may
occur in minor (`0.y`) versions; they are called out explicitly below.

## Unreleased (proposed v0.7.0)

Parity with the Tether daemon API as of 2026-10-01. All additive: no existing
exported symbol changes signature or behavior.

### Added

- **`Client.Notify(ctx, NotifyRequest) (NotifyResult, error)`** over `POST /messages/notify`: stores a message and wakes a live session of the recipient. `NotifyResult` carries `message`, `unread_count`, `wake_attempted`, `wake_delivered`, `session_id`, `wake_error` and `wake_reason`. `wake_delivered` means the reminder turn was submitted, not that the message was read.
- **`WakeReason*` constants**, the full `wake_reason` vocabulary: `busy`, `offline`, `offline-race`, `stale-generation`, `claim-unavailable`, `marker-write-failed`, `already-handled`, `settle-failed`, and `session-not-running` (Tether #71: the actor is bound to a session that is not running). **`Urgency*` constants** (`very-low`, `low`, `normal`, `high`).
- **`CodeTurnFailed`** (`turn_failed`, 502): the session's agent process ran the turn and exited non-zero (Tether #70). The client never retries it. Also **`CodeForbidden`** (403) and **`CodeLocked`** (423, archived group), which the group routes already returned.
- **`SessionState*` constants**, including **`SessionStateKilled`**: a stopped session now ends in `killed`, not `completed` (Tether #65). Plus **`IsTerminalSessionState(state)`** for the terminal set completed | failed | killed. `Session.State` stays a plain string.
- **`SessionStateChange`** and **`ParseSessionStateChange(payloadJSON)`** to decode a `session.state_changed` event's payload (`from`, `to`, `exit_code`, `reason`); **`EventKindSessionStateChanged`**.
- **`IsNotFound(err)`**: true for the daemon's 404 `not_found`, which `POST /sessions` now returns for an unknown launch instead of a 500 (Tether #67).

### Notes

- Branch on the session state, not the exit code: a stopped process may exit 0 or -1. `WaitSession` still returns only the exit code; call `GetSession` after it for the state.
- The daemon's inbox `as_session` parameter (Tether #69) is deliberately not exposed. Only a Tether-launched session that is itself the recipient may send it (the daemon's MCP proxy does); a client listing someone's inbox must not settle deliveries.

## v0.6.0 — 2026-09-30

Idempotent launch and resume, mirroring the daemon's idempotency surface
(Tether PR #59). All additive: no existing exported symbol changes signature.

### Added

- **`LaunchRequest.IdempotencyKey`** (`idempotency_key`, optional; at most 512 bytes, no surrounding whitespace or control characters, else the daemon answers 400). Used by `CreateSessionWithInput` and `LaunchWithInput`.
- **`LaunchResponse.Replayed`** (`replayed`, always present): true when the daemon answered from an earlier keyed request. A keyed `LaunchSession` on a session already past `created` returns 200 with `Replayed` true; an unkeyed one is still a 409 `conflict`.
- **`Client.ResumeLogicalAgent(ctx, logicalAgentID, ResumeOptions)`** over `POST /logical-agents/{id}/resume`. `ResumeOptions` is just `{IdempotencyKey}`; the endpoint takes no checkpoint or parent parameters, and a zero value sends no body, as the endpoint has always allowed.
- **`CodeIdempotencyConflict`** (`idempotency_conflict`, a 409 for a key reused with a different request) and **`CodeProviderSessionLost`** (`provider_session_lost`).

### Changed

- The create methods and `ResumeLogicalAgent` accept both 201 (fresh) and 200 (replay). Previously only 201 succeeded, which no daemon that predates idempotency ever contradicted.

- The repository is now licensed under MIT, replacing the earlier placeholder notice.

### Notes

- Keys are one global space with no owner field; callers namespace their own keys.
- A replay returns the bound session as it currently stands, including failed or stopped. The digest covers the request as sent; for resume it is `{op, logical_agent_id}`.
## v0.5.1 — 2026-09-30

### Fixed

- `StreamEvents` no longer ends the subscription when an SSE event carries a non-numeric `id:` field. An SSE id is an opaque string, and the stream used to abort with `parse SSE id ...` on the first one that was not an integer. Such an event is now delivered with `Seq` 0, as if it had no id, and the stream continues. Tether's own ids are numeric, so nothing changes against the current daemon. This is a patch release: the only behavior that differs is a case that previously returned an error, and no exported symbol changes.

## v0.5.0 — 2026-09-30

### Changed

- The new group, workstream and session-workstream methods refuse an empty id with an error instead of sending it: an empty path segment would have been a request to the neighbouring collection route (`/groups/`, `/workstreams/`), whose answer a caller could mistake for the one it asked for.

### Added

Three surfaces that until now existed only as MCP tools, over the daemon routes
they proxy. All additive: no existing exported symbol changes.

- **`Client.Registry()`** (`RegistryClient`): `Register`, `Lookup`,
  `LookupWithInclude`, `LookupBy`, `LookupByWithInclude`, `Search`,
  `UpdateSelf`, `Deregister`, `Merge` and `Sync` over `/registry/...`. `Sync`
  returns `(profile, synced, err)`: the daemon answers 204 for an entry with no
  callback and 200 with the refreshed profile otherwise. Types: `RegistryProfile`,
  `RegistryKind`, `RegistryStatus`, `RegistryFilter`, `RegistryUpdatePatch`,
  `RegistryArrayPatch[T]` and the skill, link, callback and external-id shapes.
- **`Client.Groups()`** (`GroupsClient`): `Create`, `Lookup`, `ListForMember`,
  `Archive`, `AddMember`, `RemoveMember`, `Leave`, `SetMemberRole`, `ListMembers`,
  `Send`, `ListMessages`, `MarkRead` and `Mentions` over `/groups` and
  `/mentions`. A send whose @-mention matches several entries returns a
  `*GroupAmbiguousMentionError` carrying the token and candidate URNs; it also
  matches `errors.Is(err, &APIError{StatusCode: 400})`.
- **Workstreams**, flat on `Client` like the daemon's own route layout:
  `CreateWorkstream`, `GetWorkstream`, `ListWorkstreams`,
  `AssignSessionWorkstream`, `EnsureSessionWorkstream`,
  `SessionWorkstreamNamespace`, `SessionDigest`, `WorkstreamDigest` and
  `WorkstreamsForRef`, with `Workstream`, `DigestQuery` and the `DigestResponse`
  tree.

Errors are `*APIError` throughout, so `errors.Is(err, &APIError{StatusCode: 404})`
and the `ErrDaemonUnreachable` wrap behave as they do for every other method.

### Notes

- The daemon's MCP-only routes that sit next to these are deliberately not
  covered: registry bindings, bootstrap and reonboard, and the session-ref routes.
- `SessionWorkstreamNamespaceOptions` has `Project`, `Owner` and `Tail` only; the
  daemon's legacy `user`/`type` parameters are not exposed.
- `messaging_store.go` is untouched.

## v0.4.0 — 2026-09-30

### Added

- **`Client.SessionHealth(ctx, sessionID)`** over `GET /sessions/{id}/health`.
  It returns a `RuntimeHealthResponse` with the session's liveness, PID,
  live state, current turn, provider identity and capabilities.
- **`CapabilitiesDTO`**: the provider capability flags carried in that
  response (`PTY`, `StreamingStdio`, `JsonRpcStdio`, `ServeHTTP`, `Resize`,
  `ProviderSessionID`, `CheckpointResume`, `BinaryRequired`).

### Changed

- The module's `go` directive is now `1.26.6`, the portfolio floor. Consumers
  on an older toolchain must upgrade to build against this version.
- `golang.org/x/sys` (indirect, via go-messaging's SQLite driver) is bumped from
  v0.22.0 to v0.48.0. That clears GO-2026-5024, which `govulncheck ./...`
  reported as present in a required module but not reachable from this code.

## v0.3.0 — 2026-09-12

### Added

- **Typed durable-delivery wrappers** — `ClaimMessage`, `AckMessage` and
  `NackMessage` over `POST /messages/{id}/claim|ack|nack`, which were
  raw-HTTP only until now (CW-20260907-0038). These are the primitive
  behind durable host acceptance: `Claim` takes custody, `Ack` reports a
  stage (`host_accepted`, then `consumed`), `Nack` declines. Consume is
  still the right call when one call is honest about what happened; this
  cycle is for when taking custody and reporting the outcome must be
  separable so a crash between them is recoverable.
- `ClaimOptions` (`Holder`, `LeaseSeconds`) and `NackOptions`
  (`Retryable`, `Reason`, `NextAttemptSeconds`). Both zero values are
  valid: `ClaimOptions{}` lets the daemon default holder and lease, and
  `NackOptions{}` dead-letters, because a Nack that says nothing is not
  a request to retry.

  Two behaviors worth knowing before you build on these:

  - `ClaimMessage` returns the lease duration the daemon **granted**,
    which may be shorter than requested — the daemon clamps to its own
    maximum. Honor the returned value, not the requested one.
  - A non-retryable `NackMessage` dead-letters, and that is a
    **successful** outcome: it returns a nil error along with the
    resulting delivery. Read `RecipientDelivery.Status` to see what
    happened rather than treating a nil error as "still alive". A caller
    that reads non-nil-error-means-failure would retry something already
    dead-lettered.

### Changed

- `go-messaging` bumped to **`v0.5.2`**, which is **required** for these
  wrappers rather than incidental. Before it, `delivery.RecipientDelivery`
  and `delivery.Attempt` could not be decoded whenever no binding was
  set — every response these three routes return — because a zero
  `Address` marshaled to `"msg:////"` and failed its own unmarshal. The
  wrappers were written against v0.5.1 and could not decode a single
  real response; see that release's notes.

### Notes

- Importing `go-messaging/delivery` for `LeaseRef` links
  `modernc.org/sqlite` into consumers, because `delivery/` bundles the
  types with the SQLite driver in one package. The module was already in
  this client's graph via go-messaging's own requirements, so this moves
  it from *required* to *linked*. Mirroring the types locally was
  considered and rejected: `RecipientDelivery` and `Attempt` are 15 and
  17 fields over six supporting types, and duplicating a durability
  contract inside the library whose purpose is to stop consumers
  hand-rolling it is self-defeating. Splitting the types out of
  `delivery/` is the real fix and belongs upstream.

## v0.2.0 — 2026-09-11

### Fixed
- **Breaking-if-you-relied-on-the-old-behavior**: `Get` and `Thread` now
  require the `Client` to be constructed with the new `WithSelfURN` option
  (see below) and return `ErrSelfURNRequired` otherwise; `Inbox` and
  `Subscribe` now always attach `?as=<to's own URN>` to the outgoing
  request. Tether's daemon requires every messaging read to assert a
  caller identity via `?as=` (ADR 0045) — this client previously omitted
  it entirely on all four calls, so every one of them was rejected
  (`400 invalid_request`) by a current Tether daemon. `Send`/`Consume`/
  `Cancel` were unaffected (`Consume` already asserted the recipient
  correctly).

### Added
- `WithSelfURN(urn string)` client option: configures the caller's own
  asserted identity, used by `Get`/`Thread` (which have no
  recipient/address parameter of their own to derive `?as=` from — unlike
  `Inbox`/`Subscribe`, which already take an explicit recipient and now use
  it directly). Required for `Get`/`Thread`; not needed for
  `Send`/`Inbox`/`Consume`/`Cancel`/`Subscribe`.
- `ErrSelfURNRequired`: returned by `Get`/`Thread` when `WithSelfURN` was
  not configured, instead of the call reaching the daemon and getting a
  less specific `400`.
- Session bootstrap (messaging-vnext T08): the provider-neutral local
  bootstrap/registration helper a launcher/host invokes at the launch
  boundary.
  - `ResolveSessionBootstrap` — resolves a session's canonical identity
    (preassigned via `SESSION` env var, explicit option, or minted
    fallback) and best-effort registers it with a running daemon. Always
    returns a usable `SessionID` even when Tether is completely
    unreachable (`Result.Registered` / `Result.RegisterErr` report the
    registration outcome separately from the local resolution, which
    never fails for daemon-reachability reasons).
  - `Client.BootstrapSession` — the lower-level `POST /sessions/bootstrap`
    wire call for a caller that has already decided the full payload.
  - `CanonicalSessionEnvKey`, `BootstrapIntent`, `PublicationChoice`,
    `ProviderMapping`, `BootstrapOptions`, `BootstrapResult`,
    `SessionBootstrapRequest`, `SessionBootstrapResult`.
  - Idempotent: a repeated call for the same preassigned session id never
    errors and never invents a competing identity.

### Changed
- `go-messaging` bumped from `v0.2.0` to `v0.5.1`, resolving the deferral
  this changelog previously recorded. `v0.5.1` is the version Tether's own
  daemon runs, so a consumer no longer links two different copies of the
  shared contract. The upgrade is source-compatible here: the package's
  `messagingtest` conformance suite passes unchanged against the
  `messaging.Store` implementation in `messaging_store.go`.
- **Minimum Go raised from 1.22 to 1.26.2**, with `toolchain go1.26.6`, to
  match Tether's own floor. At `go 1.22` this module resolved to a
  standard library with 13 known vulnerabilities reachable from its own
  call paths (`AttachSession`/`BootstrapSession` through `crypto/x509`,
  `crypto/tls` and `net/http`); `govulncheck` is clean at the new floor.
  Every known consumer — Tether (1.26.2), Torque (1.26.6), Nanite
  (1.26.7) — already requires more than this, so nothing in the portfolio
  is constrained by the change.

### Notes
- Typed `Claim`/`Ack`/`Nack` wrappers for the durable-delivery primitives
  (`POST /messages/{id}/claim|ack|nack`) are still not in this client;
  those routes remain raw-HTTP only. Tracked as CW-20260907-0038.

## v0.1.0 — 2026-05-25

Initial public release as the successor to `go-agentmux-client`.

### Added
- New module path: `github.com/hollis-labs/go-tether-client`.
- Tether naming and defaults throughout the package and docs, including the
  default socket path `unix:~/.tether/run/muxd.sock`.
- Session creation helpers for newer daemon payloads:
  - `CreateSessionWithBootPrompt`
  - `CreateSessionWithInput`
  - `LaunchWithInput`
- Long-lived turn API:
  - `SendTurn`
- Cross-scope event history:
  - `ListEvents`
- Event query support for `since_seq`, `scope`, and `kind` filters.
- AI gateway client surface:
  - `ListAIProviders`
  - `ListAIModels`
  - `ListAIRoutes`
  - `PreviewAIRoute`
  - `ExplainAIRoute`
  - `AIChat`
  - `AIChatStream`
  - `AIAudit`
  - `AIUsage`
  - `AIBudgets`
- Tests covering:
  - unix/tcp/http transport behavior
  - SSE parsing
  - long-lived timeout behavior
  - AI stream event decoding
- Migration guide: [MIGRATION.md](./MIGRATION.md)

### Changed
- Promoted the useful `go-agentmux-client` surface under the `tether` package
  name while preserving typed daemon-client semantics.
- Long-lived operations now ignore the default short HTTP timeout and instead
  rely on caller context for cancellation:
  - `AttachSession`
  - `WaitSession`
  - `SendTurn`
  - `AIChat`
  - `AIChatStream`
  - `StreamEvents`
- `StreamEvent` uses Tether event naming:
  - `ID` -> `Seq`
  - `Event` -> `Kind`

### Deprecated
- `go-agentmux-client` is superseded by this module and should only remain as a
  migration stub until consumers switch imports.
