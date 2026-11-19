# The agent host wire (agent-host/1)

How a page talks to the server that runs the model for it. It is Basalisk's `/api/agent`
surface, reduced to what any application needs, so the same page-side client
(`@hollis-labs/ade-agent`) works against Basalisk and against this module.

The idea: **the page declares its tools with each message and runs them itself.** The server
runs the model, keeps the thread, and relays each tool call to the page and its answer back.
The server never needs to know what a chess board is.

All paths are under one prefix (default `/api/agent`). JSON bodies. The application's own
authentication applies to every route; an owner only ever sees their own thread and runs.

## Routes

| Method, path | Body | Answer |
|---|---|---|
| `GET {p}` | | `{available, ready, model}`. `ready` is false when no model is configured. |
| `GET {p}/thread` | | `{thread_id, messages: StoredMessage[], active_run}` |
| `DELETE {p}/thread` | | 204. Puts the thread away; the next message starts a new one. 409 while a run is going. |
| `POST {p}/turns` | `{text, client_id, context?, tools: ToolSpec[], auto?}` | `{run_id}`. 409 while a run is going. |
| `GET {p}/runs/{id}/events` | header `Last-Event-ID` to resume | `text/event-stream` of chatstream/v1 events. 404 or 410 once long over. |
| `POST {p}/runs/{id}/tool-results` | `{call_id, content, is_error, images?: [{media_type, data}]}` | 204 |
| `POST {p}/runs/{id}/tool-alive` | `{call_id}` | 204; 409 once the run has stopped waiting for that call |
| `POST {p}/runs/{id}/approvals` | `{approval_id, decision: "once"\|"session"\|"always"\|"deny"}` | 204 |
| `POST {p}/runs/{id}/cancel` | `{}` | 204 |

`ToolSpec` is `{name, description, input_schema, level}` with level `read | act | write |
confirm`. `read` and `act` run at once. `write` asks the person first, and their answer can
stand (`session`, `always`). `confirm` asks every time. A tool with no level is `write`.

`context` is any JSON the page wants the model to see with the message (what is in front of
the person). `auto`, when set, marks a message the application wrote and the person did not
type (a guide ended, an answer arrived); it is a short label the interface shows in its place.

`StoredMessage` is `{id, seq, role: "user"|"assistant", blocks, meta: {text?, context?, auto?},
run_id?, created_at}`, with `blocks` as the model sees them (`text`, `tool_use`,
`tool_result`).

## Events

chatstream/v1, as `hollis-labs/libs/ui/ui-go/chatstream` defines it: `run.start`,
`message.start`, `part.start` / `part.delta` / `part.end` (kinds text, thinking, tool call,
tool result), `approval.request`, `activity`, and exactly one terminal event: `run.finish`,
`run.error` or `run.abort`. Each has a `seq`; a reconnect with `Last-Event-ID: <seq>` gets
what came after.

A tool call for the page to run is a `part.end` of a tool-call part carrying

    ext: {"ade.exec": "client", "ade.client": "<client_id>", "ade.call": "<call_id>"}

with the call's name in `meta` and its input in `final`. Only the page whose `client_id`
matches runs it, then posts `tool-results`. While a call takes long the page posts
`tool-alive`; a run gives up on a call that goes quiet. (Basalisk spells the keys
`basalisk.exec`, `basalisk.client`, `basalisk.call`; the client accepts both.)

## What the host promises

- A run ends with one terminal event, whatever happens.
- A tool result that arrives after the run stopped waiting is refused (409), so a page never
  believes a late change was accepted.
- A declined approval is told to the model as a refusal; the tool is not run.
- The key and the provider's address never reach the page.
