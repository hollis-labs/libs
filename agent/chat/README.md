# Conversation wire

`github.com/hollis-labs/libs/agent/chat` is one HTTP and SSE handler for a chat
conversation that outlives any single turn and any single client. A client can
submit a turn, attach as a viewer or a controller from a cursor, take or hand
over the right to type, detach and cancel. Events on the wire are
[chatstream/v1](../../ui/ui-go/chatstream), unchanged.

It is built once and mounted by a host. The host supplies what varies:

| Injected | What it is |
| --- | --- |
| `Authorize` | Checks the caller for `read`, `control`, `takeover`. Authentication stays in the host's server. |
| `Config.Agents` | Resolves an agent name to its owner, scope and kind (and, optionally, its own runner). |
| `Config.Runner` | Runs a turn and emits chatstream events. The main thing a host implements. |
| `Config.Log` | The conversation event log, a `streamhub.Log`. One stream per conversation. |
| `Config.Control` | Conversations, attachments, the input lease and the generation. |
| `Config.Grants` | Persists standing approvals. Without it scope `always` is not offered. |
| `Config.Intercept` | Sees every submitted turn first: continue, reject, or answer it. |
| `Config.Clock`, `Config.NewID` | Time and IDs, so tests need neither sleep nor luck. |

```go
st, err := sqlitestore.Open(ctx, "chat.db") // the log and the Control in one file
svc, err := chat.NewService(chat.Config{
	Agents:  agents,   // func(ctx, agent) (chat.Agent, error)
	Runner:  runner,
	Log:     st,
	Control: st,
})
h, err := chat.NewHandler(svc, authorize)
mux.Handle("/chat/", http.StripPrefix("/chat", h))
```

Packages:

| Package | Holds |
| --- | --- |
| `chat` | The ports, `Service`, `NewHandler`, `Finalizer`. |
| `chat/sqlitestore` | A `streamhub.Log` and the built-in `Control` on SQLite ([core/store](../../core/store)). |
| `chat/memstore` | The in-memory `Control`, for tests and hosts that need no durability. |
| `chat/chattest` | Conformance suites for a `Runner` and a `Control`, a scripted runner, a manual clock. |
| `chat/examples/chatserver` | The handler with the scripted runner on localhost. |

## Routes

All routes sit under the prefix the host mounts the handler at. Bodies are JSON.

| Route | Does | Needs | Answers |
| --- | --- | --- | --- |
| `POST /agents/{agent}/conversation` | The agent's current conversation, created on first use. | `read` | `200` the conversation |
| `POST /agents/{agent}/conversation/reset` | Ends it and returns a new one. Body `{attachment}`, optional when nobody holds the lease. | `control`, and the lease if anyone holds it | `200` the new conversation; `409 run_active` |
| `GET /conversations/{id}` | Current state, with any pending approval. | `read` | `200` |
| `POST /conversations/{id}/attachments` | Attach. Body `{role: "viewer"\|"controller", stream: "deltas"\|"committed"}`. | `read`; `control` for a controller | `201 {attachment_id, head, role, stream, generation}` |
| `DELETE /conversations/{id}/attachments/{attachment}` | Detach. Releases the lease if held. Never stops a run. | `read`; `control` for a controller's attachment | `204` |
| `GET /conversations/{id}/events?attachment=&after=` | The SSE event stream. | `read` | `200 text/event-stream` |
| `POST /conversations/{id}/lease` | Acquire or renew the input lease. Body `{attachment}`. | `control`, a controller attachment | `200 {attachment, expires_at}`; `409 lease_held` |
| `POST /conversations/{id}/lease/takeover` | Take the lease from its holder. Body `{attachment}`. | `takeover` | `200 {attachment, expires_at}` |
| `POST /conversations/{id}/turns` | Submit a turn. Body `{attachment, text, context, meta, request_id}`. | `control`, the lease (acquired if free) | `202 {run_id, cursor}`; `403 lease_required`; `409 run_active`; `422` when the host refuses |
| `POST /conversations/{id}/runs/{run}/cancel` | Cancel the active run. Body `{attachment}` (or `?attachment=`). | `control`, the lease | `202 {run_id}`; `409 run_not_active` |
| `POST /conversations/{id}/approvals/{approval}` | Answer an approval. Body `{attachment, decision: "allow"\|"deny", scope: "once"\|"conversation"\|"always"}`. | `control`, the lease | `204`; `409 approval_resolved`; `422 scope_unavailable` |

Submitting a turn does not return its events; the client reads them on its
event stream. That makes a second viewer and a reconnect the same code path as
the first client. `cursor` is the conversation's head when the turn was
admitted: every event of the run has a higher `seq`.

The conversation body:

```json
{"conversation_id": "conv_…", "agent": "demo", "scope": "…", "owner": "…", "kind": "embedded",
 "generation": 1, "runner_ref": "…", "head": 16, "active_run": null,
 "lease": {"attachment": "att_…", "expires_at": "…"},
 "capabilities": {"deltas": true, "approvals": true, "always": false},
 "pending_approvals": [{"approval_id": "…", "run_id": "…", "call_id": "…", "reason": "…", "descriptor": {}, "expires_at": "…", "seq": 9}]}
```

Errors are `{"code", "message"}`:

| Status | Code | When |
| --- | --- | --- |
| 400 | `invalid_request` | A malformed body, a missing `after`, text empty, `context` over 64 KiB, `meta` not an object or over 1 KiB. |
| 403 | `forbidden` | `Authorize` refused. |
| 403 | `lease_required`, `not_controller` | The attachment does not hold the lease, or cannot. |
| 404 | `not_found`, `unknown_attachment`, `unknown_approval` | |
| 409 | `stale_attachment` | The attachment is from an older generation. The body carries `generation`. Attach again. |
| 409 | `run_active`, `run_not_active`, `lease_held`, `approval_resolved`, `request_conflict` | |
| 410 | `conversation_closed` | The conversation was reset. The body carries `conversation_id`, the successor. |
| 422 | the host's own code | `Intercept` rejected the turn. Nothing was published. |
| 422 | `scope_unavailable` | `always` with no `Grants` store, or more than `once` for an approval whose descriptor says `standing: false`. |
| 502 | `runner_error` | The runner did not accept the turn or the answer. |

## The event stream

One stream per conversation. `run.finish`, `run.error` and `run.abort` end a
run; the stream stays open for the next. Each SSE frame has the event's `seq`
as its `id`, its verb as the event name and the chatstream event as data.

- **Where a client starts.** It always says: `after=0` for the full history, a
  cursor, or `head` for new events only. On a reconnect `Last-Event-ID` wins.
  The client gets every event after the cursor and then live events, none
  missing and none repeated.
- **Committed-only.** An attachment made with `stream: "committed"` gets every
  event except `part.delta`. Its cursor is the `seq` of the last event it was
  sent, so it can switch modes or resume with the same number.
- **History comes without deltas.** For a run that had already finished when
  the stream was opened, `part.delta` is not sent, whatever the mode. A run in
  progress is sent in full.
- **Gaps.** If the log no longer holds events after the cursor, the first event
  is a `gap` (`from`, `to`, `reason`), with no `id`, and the stream continues
  from the oldest retained event.
- **A slow client is closed**, with an SSE comment naming the cursor, which is
  the `id` of the last event it was sent. It reconnects with it. A slow client
  never slows the run or another client.
- **The stream ends** after a `chat.generation` event that makes the
  attachment stale, and after `chat.closed`. A reconnect is then answered `409`
  or `410`.

Every run on the stream opens the same way: `run.start`, then the user's
message (`message.start` with role `user`, one text part, `message.end`), then
the runner's events. The user's `message.start` carries the turn's `meta` keys
in its `meta`, plus `request_id` for a submit that gave one and `origin:
"runner"` for a run the runner started. Every event of a run carries the
Service's `run_id`, whatever the runner set. A turn's `context` goes to the
runner and is never published or logged.

Activity events the Service publishes:

| Kind | Value | `run_id` |
| --- | --- | --- |
| `chat.attachment` | `{state: "attached"\|"detached", attachment, role, stream}` | none |
| `chat.lease` | `{state: "acquired"\|"released", attachment, previous?}`. Renewals are not published. | none |
| `chat.generation` | `{generation}`. Every earlier attachment is stale and the lease is gone. | none |
| `chat.closed` | `{code: "conversation_closed", conversation_id}`: the successor. Last event of the stream. | none |
| `chat.approval` | `{approval_id, decision, scope, attachment?, reason?}`; `reason` is `expired` when nobody answered. | the run's |

### `final` on `part.end`

Every `part.end` on the stream carries the part's complete content in `final`,
so a committed-only client builds each message from `part.start` and `part.end`
alone:

| Part kind | `final` |
| --- | --- |
| `text`, `reasoning`, `refusal` | `{"text": "…"}`. A reasoning signature keeps its own keys beside `text`. |
| `tool_call` | The call's full input, as JSON. |
| `tool_result` | `{"text": "…", "is_error": false}` |

The Service guarantees it for parts that were streamed: `Finalizer` accumulates
a part's deltas and fills `final` when the runner left it empty. A part that
arrives whole, with no deltas, must carry its own; the runner conformance suite
fails a runner that omits it. `Finalizer` is exported for anyone who wants the
same guarantee off the wire.

## Lease, approvals, generations

- One unexpired input lease per conversation; only a controller attachment can
  hold it. It lasts 90 seconds and is renewed by calling the lease route again.
- A submit from a controller acquires the lease when nobody holds it. Submit,
  cancel and approval answers need the lease at the moment they are applied.
- Takeover replaces the lease at once. Detach and expiry release it. Neither
  stops a run: a run with no attached client keeps going, and its events stay
  in the log.
- An approval is an `approval.request` with mode `inband`. A pending approval
  with no lease holder waits; a controller who acquires the lease later can
  answer it. It is denied when its `expires_at` passes (10 minutes unless the
  runner set one). First answer wins.
- The generation rises when the runner's side is replaced. The conversation ID,
  the log and every cursor stay; every attachment and the lease go. The
  `Control` is the only authority for the number: on a signal from
  `Runner.Replaced` the Service calls `Bump` when the Control implements
  `Bumper` (the built-in ones do) and then reads the generation. If it rose,
  the active run ends with `run.abort` (reason `generation`) and
  `chat.generation` is published. A Control that bumps in its own transaction
  simply does not implement `Bumper`.

## Turns that do not come from a client

```go
// A reflex, a trigger, a message from another agent: no attachment, no lease.
receipt, err := svc.Submit(ctx, conversationID, chat.Turn{Text: "…"})

// A run that began on the runner's side (typed into a terminal, a wake message).
err := svc.Observe(ctx, conversationID, chat.Turn{Text: typed}, run)
```

Both obey one active run per conversation. `Submit` goes through `Intercept`
and returns `ErrBusy` while a run is active. `Observe` skips `Intercept`, and
waits for the active run's terminal event, bounded by its context, instead of
failing.

## Writing a runner

```go
type Runner interface {
	Start(ctx context.Context, turn Turn) (Run, error)
	Info(ctx context.Context, conversationID string) (Info, error)
	Record(ctx context.Context, turn Turn, answer string) error
	Replaced() <-chan string
}
```

- Emit `run.start` first and exactly one terminal event last.
- Close every part you open. Set `final` on any part you emit whole.
- Do not emit the user's message.
- `Cancel` must not block; the run then ends with `run.abort`.

Check it, and a `Control` of your own, with the suites:

```go
func TestRunner(t *testing.T) {
	chattest.RunnerConformance(t, chattest.RunnerHarness{
		New:          func(t *testing.T) chat.Runner { return newRunner(t) },
		Text:         "a turn that completes",
		LongText:     "a turn that runs until canceled",
		ApprovalText: "a turn that asks for approval",
	})
}

func TestControl(t *testing.T) {
	chattest.ControlConformance(t, func(t *testing.T) chat.Control { return newControl(t) })
}
```

## Try it with curl

`examples/chatserver` serves the handler with the scripted runner, which picks
a script from the first word of the turn: `tool`, `approval`, `confirm`,
`reasoning`, `error`, `abort`, `length`, `hold`; anything else is echoed back in
streamed words. A turn starting with `/ping` is answered by the host hook and
one starting with `/no` is refused with `422`.

```sh
go run ./examples/chatserver            # listens on 127.0.0.1:8787
B=http://127.0.0.1:8787/chat

# 1. The agent's conversation, and a controller attachment.
CONV=$(curl -s -X POST $B/agents/demo/conversation | jq -r .conversation_id)
ATT=$(curl -s -X POST $B/conversations/$CONV/attachments \
  -d '{"role":"controller","stream":"deltas"}' | jq -r .attachment_id)

# 2. In a second terminal, the event stream from the start.
curl -N "$B/conversations/$CONV/events?attachment=$ATT&after=0"

# 3. Submit a turn. The submit acquires the free lease.
curl -s -X POST $B/conversations/$CONV/turns \
  -d "{\"attachment\":\"$ATT\",\"text\":\"hello wire\",\"request_id\":\"r1\",\"meta\":{\"auto\":false}}"

# 4. A committed-only viewer: the same events, without part.delta.
VIEW=$(curl -s -X POST $B/conversations/$CONV/attachments \
  -d '{"role":"viewer","stream":"committed"}' | jq -r .attachment_id)
curl -N "$B/conversations/$CONV/events?attachment=$VIEW&after=0"

# 5. Reconnect from a cursor, the way a browser does.
curl -N -H 'Last-Event-ID: 9' "$B/conversations/$CONV/events?attachment=$ATT"

# 6. An approval: the run waits until it is answered.
curl -s -X POST $B/conversations/$CONV/turns -d "{\"attachment\":\"$ATT\",\"text\":\"approval\"}"
curl -s $B/conversations/$CONV | jq .pending_approvals
curl -s -X POST $B/conversations/$CONV/approvals/ap1 \
  -d "{\"attachment\":\"$ATT\",\"decision\":\"allow\",\"scope\":\"once\"}"

# 7. Cancel a run in progress. A second submit meanwhile is 409 run_active.
RUN=$(curl -s -X POST $B/conversations/$CONV/turns \
  -d "{\"attachment\":\"$ATT\",\"text\":\"hold\"}" | jq -r .run_id)
curl -s -X POST $B/conversations/$CONV/runs/$RUN/cancel -d "{\"attachment\":\"$ATT\"}"

# 8. The lease: renew it, contend for it, take it over.
curl -s -X POST $B/conversations/$CONV/lease -d "{\"attachment\":\"$ATT\"}"
OTHER=$(curl -s -X POST $B/conversations/$CONV/attachments \
  -d '{"role":"controller","stream":"deltas"}' | jq -r .attachment_id)
curl -s -X POST $B/conversations/$CONV/lease -d "{\"attachment\":\"$OTHER\"}"          # 409 lease_held
curl -s -X POST $B/conversations/$CONV/lease/takeover -d "{\"attachment\":\"$OTHER\"}"  # 200
curl -s -X POST $B/conversations/$CONV/turns -d "{\"attachment\":\"$ATT\",\"text\":\"hi\"}"  # 403 lease_required

# 9. Detach, then reset: the old conversation answers 410 with the new ID.
curl -s -X DELETE $B/conversations/$CONV/attachments/$OTHER
curl -s -X POST $B/agents/demo/conversation/reset -d '{}'
curl -s $B/conversations/$CONV
```

Run it with `-whole` for a runner that yields whole messages (`capabilities.deltas`
is then `false` and no `part.delta` is sent), and with `-db chat.db` to keep
conversations across restarts.

## Limits

- One conversation per agent, one owner, one host process serving it.
- `request_id` is remembered in memory, for the last 256 submits of a
  conversation: a retry after a restart of the host is a new turn.
- A run does not survive a restart of the host. The next time the conversation
  is touched the run is ended with `run.abort` (reason `generation`) and, with a
  Control that implements `Bumper`, the generation rises.
- A run the Service has to end itself (a generation change, a cancel the runner
  ignores for 10 seconds, a shutdown) ends with `run.abort` and may leave parts
  open; a client treats a terminal event as closing whatever is open.
- Nothing prunes the log. Every event is stored, deltas included.
- Attachments have no expiry of their own: a client that never detaches leaves
  its record behind.
