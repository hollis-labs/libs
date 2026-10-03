package hubbind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"time"

	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
	streamhub "github.com/hollis-labs/libs/ui-go/streamhub"
)

// Publish publishes ev on stream with its verb as the event name and returns ev
// with Seq set to the cursor the hub assigned. ev.Seq is ignored on the way in.
func Publish(ctx context.Context, hub *streamhub.Hub, stream string, ev chatstream.Event) (chatstream.Event, error) {
	ev.Seq = 0
	payload, err := json.Marshal(ev)
	if err != nil {
		return ev, fmt.Errorf("hubbind: marshal %s: %w", ev.Verb, err)
	}
	rec, err := hub.Publish(ctx, stream, streamhub.Event{Name: string(ev.Verb), Data: payload})
	if err != nil {
		return ev, err
	}
	ev.Seq = uint64(rec.Seq)
	return ev, nil
}

// Terminal is a streamhub.WithTerminal predicate: a record whose name is a
// terminal verb ends the stream, so subscribers receive io.EOF after it.
func Terminal(rec streamhub.Record) bool { return chatstream.Verb(rec.Name).Terminal() }

// Decode turns a hub record back into the event it carries, with Seq set to the
// record's cursor.
func Decode(rec streamhub.Record) (chatstream.Event, error) {
	var ev chatstream.Event
	if err := json.Unmarshal(rec.Data, &ev); err != nil {
		return chatstream.Event{}, fmt.Errorf("hubbind: record %d (%s): %w", rec.Seq, rec.Name, err)
	}
	ev.Seq = uint64(rec.Seq)
	return ev, nil
}

// GapEvent renders a hub gap as a gap event. From and To are the lost cursor
// range, inclusive; a cursor beyond the head has no lost records, so From is the
// requested cursor and To the head.
func GapEvent(runID string, now time.Time, g streamhub.Gap) chatstream.Event {
	ev := chatstream.Event{V: chatstream.SchemaVersion, RunID: runID, Time: now, Verb: chatstream.VerbGap}
	switch g.Reason {
	case streamhub.GapRetention:
		ev.Reason = chatstream.GapRetention
	case streamhub.GapCursorAhead:
		ev.Reason = chatstream.GapCursorAhead
	default:
		ev.Reason = chatstream.GapDroppedSlow
	}
	if g.Reason == streamhub.GapCursorAhead {
		ev.From, ev.To = uint64(g.Requested), uint64(g.Latest)
		return ev
	}
	ev.From = uint64(g.Requested) + 1
	if g.OldestAvailable > 0 {
		ev.To = uint64(g.OldestAvailable) - 1
	}
	return ev
}

// Events yields the events of a subscription in order. A gap notice becomes a
// gap event (stamped with runID and the current time). The sequence ends
// without an error at io.EOF, when the run's terminal event has been delivered;
// any other error from the subscription, such as a slow-consumer close (resume
// from the last event's Seq), is yielded and ends the sequence.
func Events(ctx context.Context, sub streamhub.Subscription, runID string) iter.Seq2[chatstream.Event, error] {
	return func(yield func(chatstream.Event, error) bool) {
		for {
			item, err := sub.Next(ctx)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					yield(chatstream.Event{}, err)
				}
				return
			}
			var ev chatstream.Event
			if item.Gap != nil {
				ev = GapEvent(runID, time.Now(), *item.Gap)
			} else if ev, err = Decode(item.Record); err != nil {
				yield(chatstream.Event{}, err)
				return
			}
			if !yield(ev, nil) {
				return
			}
		}
	}
}
