# eventbus

Producer-side client for the shared event hub. The module is
`github.com/hollis-labs/libs/mesh/eventbus`; package name `eventbus`.

```go
pub, err := eventbus.FromEnv("cerberus", eventbus.Config{})
if err != nil { /* bad spool dir */ }
defer pub.Close(ctx)

pub.Publish(eventbus.Item{ID: ev.ID, Priority: eventbus.PriorityHigh, Body: rawJSON})
```

With `TETHER_EVENTS_URL` unset, `FromEnv` returns a no-op publisher and the app
behaves as it did before. The bearer token is read from the file named by
`HOLLIS_AUTH_TOKEN_FILE_<APP>` on every request, so rotating it needs no restart.
`TETHER_EVENTS_SPOOL_DIR` enables the disk spool.

See the package documentation for the delivery guarantees and for what this
client deliberately does not do (it is not a durable outbox). It has no
dependencies and imports nothing from the harness group; turning a
`runtimeevents.Event` into an `Item` is a few lines in the consuming module.
Subscribing to the hub's stream is not part of this version.
