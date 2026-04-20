# Task Agent Boot Prompt: go-agentmux-client

You are working in the `go-agentmux-client` module root.

Goal: turn this scaffold into the reusable Go client package for Agent Mux, published as `github.com/hollis-labs/go-agentmux-client` with package name `agentmux`.

Context:

- Source API contract lives in the sibling Agent Mux repository at `docs/api/README.md`.
- Existing non-reusable internal client lives in the Agent Mux repository at `internal/client/client.go`.
- Do not import `github.com/chrispian/agent-mux/internal/*`; this package must be consumable by Nanite, Clockwork, and other apps.
- The mux daemon local API is currently v0.0.2 and supports UDS and loopback TCP transports.

Implementation expectations:

- Keep the module dependency-light. Prefer standard library only unless a dependency pays for itself.
- Public module path: `github.com/hollis-labs/go-agentmux-client`.
- Public package name: `agentmux`.
- Preserve typed API errors with HTTP status, mux error code, and message.
- Preserve `ErrDaemonUnreachable` for socket missing / connection refused branches.
- Support listen addresses: `unix:/path`, `tcp:host:port`, empty default, and `http(s)://` for tests.
- Cover the documented endpoints with tests:
  - `GET /health`
  - session create, launch, list, get, stop, wait, input, attach, resize
  - checkpoint create/list
  - broker create/list/get/reply
  - session event history
  - `GET /events/stream` SSE
  - catalog list endpoints
- Add examples that show the Nanite host-chat smoke path over broker envelopes.
- Run `gofmt ./...` and `go test ./...`.

Quality bar:

- Keep DTO JSON tags aligned with the daemon API docs.
- Do not make users deal with HTTP plumbing for common flows.
- Do not hide response details that apps may need for routing and replay.
- Use contexts for all network operations.
- Avoid background goroutine leaks in streaming APIs.
