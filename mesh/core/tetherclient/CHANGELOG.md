# Changelog

All notable changes to `go-tether-client` are documented here. The format is
loosely based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
this project follows [Semantic Versioning](https://semver.org/). While the
major version is `0.x`, the API is considered pre-1.0 and breaking changes may
occur in minor (`0.y`) versions; they are called out explicitly below.

## Unreleased

### Added
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

### Notes
- The `go-messaging` dependency remains pinned at `v0.2.0` in this
  release. Tether's own daemon has moved past that version internally,
  but nothing added in this change depends on `go-messaging` at all, and
  bumping it is a separate, independently-reviewable change deferred
  rather than bundled in here.

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
