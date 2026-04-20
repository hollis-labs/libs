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

## Surface

The client covers:

- health
- session lifecycle, input, attach, resize, wait
- checkpoints
- broker envelopes
- historical session events
- live event SSE streaming
- catalog reads

The package intentionally has no dependency on `agent-mux/internal/*`.
