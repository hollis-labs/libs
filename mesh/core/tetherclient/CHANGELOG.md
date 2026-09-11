# Changelog

All notable changes to `go-tether-client` are documented here. The format is
loosely based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
this project follows [Semantic Versioning](https://semver.org/). While the
major version is `0.x`, the API is considered pre-1.0 and breaking changes may
occur in minor (`0.y`) versions; they are called out explicitly below.

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
