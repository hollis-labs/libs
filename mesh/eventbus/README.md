# eventbus

Producer-side client for the shared event hub. The module is
`github.com/hollis-labs/libs/mesh/eventbus`; package name `eventbus`.

```go
pub, err := eventbus.FromEnv("cerberus", eventbus.Config{})
if err != nil { /* bad spool dir */ }
defer pub.Close(ctx)

pub.Publish(eventbus.Item{ID: ev.ID, Priority: eventbus.PriorityHigh, Body: rawJSON})
```

`HOLLIS_EVENTS_URL` is the hub's base URL (`/events` is added; a URL already ending in `/events` is used as is). With it unset, `FromEnv` returns a no-op publisher and the app
behaves as it did before. The bearer token is read from the file named by
`HOLLIS_AUTH_TOKEN_FILE_<APP>` on every request, so rotating it needs no restart.
`HOLLIS_EVENTS_SPOOL_DIR` enables the disk spool.

With no token file, no Authorization header is sent (a local relay that holds the
credential needs none). Events the hub rejects, or refuses for good, are set aside
in `quarantine.jsonl` in the spool directory and reported, never counted as sent;
if the queue or spool is full, debug goes first, then activity, and protected
events only as the last resort, recorded and announced (see `Config.GapMarker`).

See the package documentation for the delivery guarantees and for what this
client deliberately does not do (it is not a durable outbox). It has no
dependencies and imports nothing from the harness group; turning a
`runtimeevents.Event` into an `Item` is a few lines in the consuming module.
Subscribing to the hub's stream is not part of this version.

## Relaying an outbox

An app that must not lose events keeps its own outbox and ships it with the
`relay` package. The relay reads the outbox in order from a saved cursor, sends
each batch with `Client.Deliver`, and moves the cursor only over events the hub
has settled (sent, or durably quarantined). A missing credential is waited out,
not quarantined. After a crash it resends from the
saved cursor; the hub dedupes on the event id.

```go
r, _ := relay.New(relay.Config{
	Source:    myOutbox,                                   // After(ctx, afterSeq, limit) ([]relay.Entry, error)
	Cursor:    relay.FileCursor{Path: "/var/lib/app/relay.cursor"},
	Deliverer: client,                                     // an *eventbus.Client
})
go r.Run(ctx)  // returns when ctx is done
```
