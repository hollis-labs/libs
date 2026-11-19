package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"context"

	chatstream "github.com/hollis-labs/libs/ui/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui/ui-go/chatstream/hubbind"
	streamhub "github.com/hollis-labs/libs/ui/ui-go/streamhub"
)

// Activity kinds the Service publishes. The first four are conversation-level
// and carry no run_id; chat.approval belongs to its run.
const (
	// ActivityLease: the input lease changed hands. Value: state ("acquired"
	// or "released"), attachment, and previous when a holder was displaced.
	// Renewals are not published, and a generation change drops the lease
	// without a chat.lease event.
	ActivityLease = "chat.lease"
	// ActivityAttachment: a client attached or detached. Value: state
	// ("attached" or "detached"), attachment, role, stream.
	ActivityAttachment = "chat.attachment"
	// ActivityGeneration: the runner's side was replaced. Value: generation.
	// Every earlier attachment is stale and the lease is gone.
	ActivityGeneration = "chat.generation"
	// ActivityClosed: the conversation was reset. Value: code
	// ("conversation_closed") and conversation_id, the successor. It is the
	// last event of the stream.
	ActivityClosed = "chat.closed"
	// ActivityApproval: an approval was resolved. Value: approval_id,
	// decision ("allow" or "deny"), scope, attachment (who answered) and
	// reason ("expired" when nobody did).
	ActivityApproval = "chat.approval"
)

// Abort reasons the Service uses on a run.abort it publishes itself.
const (
	AbortCanceled   = "canceled"
	AbortGeneration = "generation"
	AbortShutdown   = "shutdown"
)

// Run error codes the Service uses on a run.error it publishes itself.
const (
	// CodeRunnerError: the runner's event sequence failed.
	CodeRunnerError = "runner_error"
)

// ProviderHost is the provider on the run.start of a turn the host answered.
const ProviderHost = "host"

// OriginRunner is the user message's meta "origin" for a run the runner
// started (see [Service.Observe]).
const OriginRunner = "runner"

func activity(kind string, value any) chatstream.Event {
	return chatstream.Event{Verb: chatstream.VerbActivity, Kind: kind, Value: mustJSON(value)}
}

// textFinal is the final of a text part that carries text.
func textFinal(text string) json.RawMessage {
	return mustJSON(map[string]string{"text": text})
}

// messageEvents is one whole message with a single text part.
func messageEvents(messageID, partID, role, text string, meta map[string]json.RawMessage) []chatstream.Event {
	return []chatstream.Event{
		{Verb: chatstream.VerbMessageStart, MessageID: messageID, Role: role, Meta: meta},
		{Verb: chatstream.VerbPartStart, PartID: partID, Kind: string(chatstream.PartText)},
		{Verb: chatstream.VerbPartEnd, PartID: partID, Final: textFinal(text)},
		{Verb: chatstream.VerbMessageEnd, MessageID: messageID},
	}
}

// Cursor says where an event stream starts.
type Cursor struct {
	head bool
	seq  uint64
}

// After starts after the event with that seq; After(0) is the full history.
func After(seq uint64) Cursor { return Cursor{seq: seq} }

// FromHead starts at the head: new events only.
func FromHead() Cursor { return Cursor{head: true} }

// Stream is one attachment's view of a conversation's events. It is not safe
// for concurrent use; Close may be called from any goroutine.
type Stream struct {
	sub        streamhub.Subscription
	generation uint64
	now        func() time.Time
	last       uint64
	ended      bool
}

// Next blocks for the next event. A gap in the log arrives as a gap event with
// Seq zero. It returns io.EOF once the stream has ended: after a
// chat.generation event that makes the attachment stale, or after chat.closed.
// A *streamhub.SlowConsumerError means the reader fell behind and was closed;
// resume from [Stream.Last].
func (st *Stream) Next(ctx context.Context) (chatstream.Event, error) {
	if st.ended {
		return chatstream.Event{}, io.EOF
	}
	item, err := st.sub.Next(ctx)
	if err != nil {
		var slow *streamhub.SlowConsumerError
		if errors.Is(err, io.EOF) || errors.As(err, &slow) || ctx.Err() != nil {
			return chatstream.Event{}, err
		}
		return chatstream.Event{}, fmt.Errorf("chat: read conversation stream: %w", err)
	}
	if item.Gap != nil {
		return hubbind.GapEvent("", st.now(), *item.Gap), nil
	}
	ev, err := hubbind.Decode(item.Record)
	if err != nil {
		return chatstream.Event{}, fmt.Errorf("chat: decode event: %w", err)
	}
	st.last = ev.Seq
	if ev.Verb == chatstream.VerbActivity {
		switch ev.Kind {
		case ActivityClosed:
			st.ended = true
		case ActivityGeneration:
			var v struct {
				Generation uint64 `json:"generation"`
			}
			if json.Unmarshal(ev.Value, &v) == nil && v.Generation > st.generation {
				st.ended = true
			}
		}
	}
	return ev, nil
}

// Last is the seq of the last event Next returned: the cursor to resume from.
func (st *Stream) Last() uint64 { return st.last }

// Close ends the stream.
func (st *Stream) Close() { _ = st.sub.Close() }

// deltaFilter drops part.delta for a committed-only attachment, and for every
// run that had already finished when the stream was opened: records at or
// below head that do not belong to the run active at that moment.
func deltaFilter(mode StreamMode, head streamhub.Seq, active string) func(streamhub.Record) bool {
	return func(rec streamhub.Record) bool {
		if rec.Name != string(chatstream.VerbPartDelta) {
			return true
		}
		if mode == StreamCommitted {
			return false
		}
		if rec.Seq > head {
			return true
		}
		if active == "" {
			return false
		}
		var e struct {
			RunID string `json:"run_id"`
		}
		return json.Unmarshal(rec.Data, &e) == nil && e.RunID == active
	}
}
