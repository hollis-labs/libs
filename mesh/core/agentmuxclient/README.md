# go-agentmux-client

Go client for the Agent Mux local HTTP API.

Module:

```text
github.com/hollis-labs/go-agentmux-client
```

Package:

```go
import agentmux "github.com/hollis-labs/go-agentmux-client"
```

## Example

```go
ctx := context.Background()
client := agentmux.MustNew("") // default unix:~/.agent-mux/run/muxd.sock

health, err := client.Health(ctx)
if err != nil {
    return err
}
_ = health

env, err := client.CreateEnvelope(ctx, agentmux.EnvelopeCreateRequest{
    Sender:      "nanite",
    Recipient:   "relay",
    WorkflowID:  "mux-smoke-test",
    MessageType: "request",
    Payload:     `{"prompt":"hello"}`,
})
if err != nil {
    return err
}
_ = env
```

## Transport

`New` accepts the same listen address forms as `muxd`:

- `unix:/absolute/path`
- `tcp:127.0.0.1:7180`
- `http://host:port` for tests and proxies

Passing an empty listen address uses `unix:~/.agent-mux/run/muxd.sock`.

## Messaging (v0.2.0)

The client implements the `go-messaging` Store and Dispatcher contracts over
the daemon's `/messages/*` HTTP routes.

```go
import (
    agentmux "github.com/hollis-labs/go-agentmux-client"
    messaging "github.com/hollis-labs/go-messaging"
)

client := agentmux.MustNew("")

// Store — send, receive, thread, consume, cancel.
store := client.AsStore() // messaging.Store

// Dispatcher — request/reply semantics via POST /messages/request.
disp := client.AsDispatcher() // messaging.Dispatcher

ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

resp, err := disp.Request(ctx, messaging.Envelope{
    From:    messaging.Address{Kind: messaging.KindAgent, Authority: "nanite", ID: "alice"},
    To:      messaging.Address{Kind: messaging.KindAgent, Authority: "agent-mux", ID: "relay"},
    Payload: json.RawMessage(`{"prompt":"hello"}`),
})
```

`Subscribe` on `AsStore()` is best-effort in-process fan-out: it observes
envelopes sent through the same Store instance. `AsDispatcher()` overrides
`Request` to use the daemon's blocking HTTP endpoint instead.

## Surface

The client covers:

- health
- session lifecycle, input, attach, resize, wait
- checkpoints
- broker envelopes (v0.1 legacy)
- messaging.Store + Dispatcher over /messages/* (v0.2)
- historical session events
- live event SSE streaming
- catalog reads

The package intentionally has no dependency on `agent-mux/internal/*`.
