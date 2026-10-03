// Package streamhub is a replayable fan-out hub over opaque payloads: a
// per-stream cursor, an explicit gap signal, replay from a Log, and
// per-subscriber slow-consumer policies. It has no dependencies beyond the
// standard library and knows nothing about what a payload means.
//
// # Model
//
// A stream is a named, append-only sequence of records. A [Log] stores it and
// assigns each record a [Seq] (1, 2, 3, ... per stream). A [Hub] sits on a Log:
// [Hub.Publish] appends and delivers to live subscribers; [Hub.Subscribe]
// delivers everything after a cursor (replay from the log) and then live
// records, with no record missing and none repeated at the boundary. A
// payload is an [Event]: a Name plus opaque Data.
//
//	hub := streamhub.New(streamhub.NewMemoryLog())
//	hub.Publish(ctx, "session-1", streamhub.Event{Name: "delta", Data: b})
//	sub, _ := hub.Subscribe(ctx, "session-1", streamhub.SubscribeOptions{After: lastSeen})
//	item, err := sub.Next(ctx)
//
// [NewMemoryLog] is a bounded in-memory ring per stream. A durable backend is
// an application's own [Log] implementation over its tables; the hubtest
// sub-package holds the conformance suite it must pass.
//
// # Subscribe
//
// Subscribe registers the subscriber in a pending state and reads the stream
// head under the stream lock, then releases the lock. Replay reads Log.After up
// to that head outside the lock, paced by the consumer. Records published in the
// meantime (Seq above the head) queue on the subscriber and are delivered after
// the replay, de-duplicated by Seq. Publish appends and fans out as one step per
// stream, so the head a subscriber reads always splits history exactly. A slow
// Log.After therefore never blocks Publish or other subscribers.
//
// # Gaps
//
// If the log has dropped records after the cursor, the subscriber receives an
// [Item] whose Gap is set (reason [GapRetention]), and replay continues from the
// oldest retained record; a cursor beyond the head reports [GapCursorAhead] the
// same way. With [SubscribeOptions].GapAsError the subscription instead ends
// with a [*GapError]. A subscriber that loses records to its own slow-consumer
// policy gets a [GapDropped] gap with the exact count where the loss happened.
// A Log never returns a partial result: it yields [ErrGap] or [ErrCursorAhead]
// before any record.
//
// # Slow consumers
//
// A subscriber's live buffer is bounded. The policy decides what happens on
// overflow; see [SlowPolicy]. The default, [CloseAndResume], loses nothing
// while the log retains: the subscription closes with a [*SlowConsumerError]
// that carries the cursor to resume from.
//
// # Terminal records
//
// [WithTerminal] gives the hub a predicate that recognizes a stream's last
// record. After it, [Hub.Publish] returns [ErrTerminated], subscribers get
// io.EOF once drained, and a late subscriber replays through the terminal
// record. [WithFinalizer] on [Hub.Close] synthesizes one if the producer never
// did. An ended stream stays replayable for [WithRetainAfterClose] (60s by
// default), then is forgotten.
package streamhub
