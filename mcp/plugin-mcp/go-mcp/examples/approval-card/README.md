# Gatecrasher approval card

A runnable stdio MCP server with one read-only tool, `show_approval_card`, and
one bundled HTML resource, `ui://gatecrasher/approval-card`. Gatecrasher owns
card content and decisions. This example only renders its transport-neutral
`gatecrasher.card/v1` format and sends actions from the browser.

From the `mcp/plugin-mcp` module directory:

```sh
go run ./go-mcp/examples/approval-card \
  -card ./go-mcp/examples/approval-card/card.json \
  -gate-origin https://gate.example \
  -intake-origin https://intake.example
```

Configure that command as a stdio MCP server in an Apps-capable host, then call
`show_approval_card`. The included fixture demonstrates rendering; replace it
with an actual Gatecrasher `GET /api/gate/v1/approvals/{id}/card` response for
real decisions. Restart with a freshly fetched card to refresh the display.
The example never calls a service from the MCP server, never creates approvals,
and needs no server-side owner credential. Origins are trusted operator
configuration, fixed when launching the server. They must use HTTPS, except
loopback HTTP for local testing. Do not supply credential-bearing URLs as origin
flags or store credentials in the card file.

The UI shows the title, summary, tier, expiry, principal, requested grant,
evidence, prompt and terminal outcome. It follows the card's actions:

| Card type | Client action |
| --- | --- |
| `approve` | Approve or decline with `outcome` |
| `choose` | Submit the selected `prompt.choices` ID as `choice`, or decline |
| `answer` | Submit the text as `answer`, or decline |
| `request_secret` | POST the form directly to `secret.post_url`, or decline using decide |

Decisions POST JSON to the card's `action.decide`, constrained to the configured
Gatecrasher origin and `/api/gate/v1/approvals/{approval_id}/decide` path. The
owner enters their bearer credential into the **client** password input. It is
held only in the view's memory, attached only to the Gatecrasher request, and
cleared after submission. It is never requested as a tool argument, forwarded
to an intake, sent over postMessage, or persisted. A trusted production host may
replace this manual entry with its client credential facility; this example
does not invent an AppBridge method for accessing host credentials.

The secret password input sends a URL-encoded form field named `value` directly
to the configured intake origin at `secret.post_url`. It clears the field before
awaiting the request. It does not read or forward the intake response body.
The producer confirms the write to Gatecrasher and supplies `response.secret_ref`;
the UI does **not** approve a secret request through decide or call
`/secret-provided` itself. Decline sends only `{"outcome":"denied"}`. Neither
secret values nor credentials enter tool arguments/results, model messages,
logs, analytics, storage, or context updates. Normal `answer` text is a decision
payload, so it must not be used for secret entry.

Browser fetch uses `credentials: omit`, `redirect: error`, `cache: no-store` and
`referrerPolicy: no-referrer`. The producer-owned intake must authorize direct
client submission (for example an intake capability URL); the Gatecrasher owner
bearer is not intake authorization. Both APIs need CORS for the host's sandbox
origin, and the host must permit the configured CSP connect domains. This PR
does not change either service's CORS or authorization configuration. A CSP or
CORS failure is an honest UI failure; no server relay is attempted.

A 202 remains a shadow decision awaiting the owner; 409 requires a refresh; 403
is shown as unauthorized. Failed or uncertain submissions are never replayed.
The view uses text nodes for card content, accepts messages only from its parent,
and performs the Apps initialize/initialized handshake before rendering tool
results. No tools/call, ui/message or model-context notification is emitted by
form submission. Terminal and expired cards have no controls. The browser
checks expiry again on click; the API remains authoritative.

## Verification

```sh
go test -race -count=1 ./go-mcp/ui ./go-mcp/examples/approval-card
```

For the standalone Node invocation, change into the example directory first and
run `node --test app.test.cjs`. Node 22+ is required by the Go test gate. The
executable test evaluates the **actual bundled app.js** with a small DOM and two
real local HTTP servers. It exercises all four card kinds, owner bearer routing,
choice/answer bodies, decline, terminal/expired cards, origin/path rejection,
and 202/403/409/failure behavior. A randomly generated secret reaches only the
intake form endpoint; the test checks captured host messages (the only outbound
transcript channel), unchanged tool results, logs, and cleared fields. The wire
test drives actual MCP tools/list, resources/read and tools/call. This is a
transport/client-flow test, not a claim of end-to-end validation in Claude or
Codex, or against live Gatecrasher/Warden.

Contract sources: Gatecrasher [`api/openapi/gate-v1.yaml`](https://github.com/hollis-labs/gatecrasher/blob/main/api/openapi/gate-v1.yaml),
[`pkg/approvalapi/cards.go`](https://github.com/hollis-labs/gatecrasher/blob/main/pkg/approvalapi/cards.go),
and [`docs/integration-guide.md`](https://github.com/hollis-labs/gatecrasher/blob/main/docs/integration-guide.md).
Apps wire sources and version notes are in [ui/README.md](../../ui/README.md).
