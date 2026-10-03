# go-streamhub

Payload-opaque, stdlib-only replayable fan-out hub: per-stream cursor, gap signal, replay ring and slow-consumer policies.

## Status

**Pre-release.** This project is unreleased, not deployed, and has no outside consumers. It's being built in the open: the code, the docs, and this README describe what exists today, not a pitch for what's planned. Interfaces and behavior change without notice, and there are no compatibility guarantees yet.

## Install

```sh
go get github.com/hollis-labs/libs/ui-go/streamhub
```

## Usage

```go
package main

import (
	"context"
	"fmt"

	streamhub "github.com/hollis-labs/libs/ui-go/streamhub"
)

func main() {
	ctx := context.Background()
	hub := streamhub.New(streamhub.NewMemoryLog())
	defer hub.Shutdown(ctx)

	hub.Publish(ctx, "session-1", streamhub.Event{Name: "delta", Data: []byte("hello")})
	hub.Publish(ctx, "session-1", streamhub.Event{Name: "delta", Data: []byte("world")})

	// Replay everything after cursor 1, then keep listening for live records.
	sub, err := hub.Subscribe(ctx, "session-1", streamhub.SubscribeOptions{After: 1})
	if err != nil {
		panic(err)
	}
	defer sub.Close()

	item, err := sub.Next(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(item.Record.Seq, item.Record.Name, string(item.Record.Data))
}
```

The same program is the package example in [`example_test.go`](./example_test.go),
which `go test` runs and checks.

A `Hub` sits on a `Log`. `streamhub.NewMemoryLog()` is a bounded in-memory
ring per stream; a durable backend is your own `Log` over your own tables, and
[`hubtest.Conformance`](./hubtest) is the suite it must pass. Payloads are an
`Event{Name, Data}` of opaque bytes; the cursor, `Seq`, is a per-stream
`uint64` starting at 1 that the log assigns.

## How Subscribe works

The point of the library is the replay-to-live handover. `Subscribe`:

1. Under the stream lock, registers the subscriber in a **pending** state and
   reads the stream head. Then it releases the lock.
2. **Outside the lock**, replays `Log.After(cursor)` up to that head, paged and
   paced by the consumer (a consumer that reads slowly slows only its own replay).
3. Records published meanwhile (Seq above the head) queue on the subscriber.
4. When replay is done the subscriber goes live and the queue is drained,
   **de-duplicated by Seq**.

`Publish` appends and fans out as one step per stream, so the head seen in step
1 splits history exactly: nothing is missed, nothing is delivered twice. Because
replay runs outside every lock, a slow `Log.After` (a big table read) cannot
stall `Publish` or other subscribers. Admission is closed under the same mutex
as the subscriber set, so no record is ever sent to a subscription that is
being torn down. Start with `After: streamhub.FromLatest` for live-only.

A gap is a value, not a silent skip. If the log has pruned records after your
cursor you get an `Item` with `Gap` set (`GapRetention`) and replay continues from
the oldest retained record; a cursor past the head is `GapCursorAhead`. Set
`SubscribeOptions.GapAsError` to get a `*GapError` (matching `ErrGap`) and end
the subscription instead. What a gap looks like on the wire is left to an encoder.

## Slow consumers

Each subscription has a bounded live buffer (`Buffer`, default 256) and a
`SlowPolicy`. The policy applies to live records; replay is consumer-paced and
never lossy. What each policy guarantees:

| Policy | Publish waits? | What the subscriber can lose |
|---|---|---|
| `CloseAndResume` (default) | never | Nothing, while the log retains. The subscription drains its buffer, then `Next` returns a `*SlowConsumerError` (matches `ErrSlowConsumer`) whose `LastDelivered` is the cursor to resume from. |
| `Block` | yes, until room or the publisher's context ends | Nothing while `Publish` waits. If the publisher's context ends first, that record is dropped for this subscriber and reported as a `GapDropped` gap; `Publish` returns the record with the context error. |
| `DropNewest` | never | The incoming records once the buffer is full. A `GapDropped` gap with the exact count arrives after the buffered records. |
| `DropOldest` | never | The oldest buffered records. A `GapDropped` gap with the exact count arrives where they were. |
| `EvictAfterN(n)` | never | As `DropOldest`, then the subscription is closed with a `*SlowConsumerError` on the n-th consecutive drop (Tether uses 64). |

`Gap.Missed` is exact for `GapDropped`; `Subscription.Drops()` counts the same
losses. Lossy policies never lose silently: every loss surfaces as a gap.

## Terminal records and closing

`WithTerminal(func(Record) bool)` tells the hub which record ends a stream (the
hub does not know your vocabulary). After it, `Publish` returns `ErrTerminated`,
each subscriber drains and then gets `io.EOF`, and a late subscriber replays
through the terminal record. `Hub.Close(ctx, stream, WithFinalizer(...))` ends a
stream and, if no terminal was published, publishes a synthesized one first. An
ended stream stays replayable for `WithRetainAfterClose` (60s by default), then
the hub forgets it (and asks the `Log` to, if it implements `Forgetter`).

## Compatibility

This module is pre-1.0: minor releases may break the exported API. Pin an exact
version, and read [CHANGELOG.md](./CHANGELOG.md) before upgrading — every
breaking change is listed there.

## Out of scope

- Any SQL or on-disk backend: applications implement `Log` over their own
  tables and validate it with `hubtest.Conformance`. The core stays stdlib-only.
- Wire frames: SSE/JSON encoding, keepalives, the shape of a gap or head frame,
  `Last-Event-ID` handling. A `Gap` is an in-process value; an encoder renders it.
- The event model: payloads are `[]byte` plus a name. No event vocabulary,
  decoders, reducers, or terminal vocabulary (you supply the predicate).
- Presence, single-subscriber takeover and session indexes: application concerns
  (close the old `Subscription` yourself).
- A dedupe/identity ledger and coalescing or phased-vs-live buffering:
  application policy that is visible on the wire.
- A multi-topic replayable multiplexer with a composite cursor. Use one stream
  with a `Filter` instead (see below).
- Byte-offset cursors (agentkit's session attach is a different cursor domain)
  and `mcp.EventStore` (its `Append` cannot allocate the cursor).

## Known limitations / open questions

- **Retention is per stream, not per event class.** The intended pattern for a
  multiplexed session is one hub stream per session, the channel in
  `Event.Name`, and a per-subscription `Filter` selecting channels, so a single
  `Seq` covers everything. But `Retention` (records, bytes, age) trims oldest
  first regardless of channel, so a high-volume delta channel can evict
  low-volume control events (approvals, terminal state) from the log before a
  reconnecting client replays them. This is open and not solved here; candidate
  answers are retention by event class or two logs (one per class). Until it
  is decided, do not rely on replay alone for events that must survive a burst.
- `Filter` runs inside the hub's fan-out (under the stream lock) and in the
  replay goroutine. It must be fast, must not block, and must not call the Hub.
- `Next` is meant for one goroutine per subscription.
- `MemoryLog` is not durable, and its `Reopen` in the conformance suite is the
  same log; real reopen behavior is checked against a second test backend only.
- When a hub starts over a populated `Log`, it decides whether a stream had
  already ended by applying the terminal predicate to the last retained record.
- `Log.Trim` runs after each `Publish` when `WithRetention` is set. If it fails,
  `Publish` returns the (published) record together with the error.
- Slow-consumer detection is by buffer occupancy; there is no time-based
  timeout or keepalive.

## Development

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
go test -race -count=20 -run Concurrency ./...   # the concurrency suite
go test -run xxx -bench PublishFanout ./...      # 100 fast + 1 slow subscriber
```

CI (`.github/workflows/check.yml`) is the full gate. The tests use
`testing/synctest`, so anything that depends on time runs on virtual time and
is deterministic.

## License

MIT — see [LICENSE](./LICENSE).
