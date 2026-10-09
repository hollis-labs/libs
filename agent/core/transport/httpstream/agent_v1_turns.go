// Package httpstream writes authenticated host-owned per-run canonical events.
// It registers no routes and supplies no credentials; host middleware owns
// authorization, off-box configuration and view/run lookup before Write.
package httpstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui-go/chatstream/hubbind"
	"github.com/hollis-labs/libs/ui-go/chatstream/sink"
	"github.com/hollis-labs/libs/ui-go/chatstream/sink/native"
	streamhub "github.com/hollis-labs/libs/ui-go/streamhub"
	agentservice "github.com/hollis-labs/substrate/agent/service"
)

var ErrForeignRun = errors.New("canonical event belongs to a different run")

type State struct {
	RunID             string
	After, Checkpoint uint64
}

// ParseCursor accepts the strict Last-Event-ID form, excluding reserved latest
// cursors and query parameter fallbacks.
func ParseCursor(r *http.Request) (uint64, error) {
	if r.URL.RawQuery != "" {
		return 0, errors.New("event cursors use Last-Event-ID only; query parameters are unsupported")
	}
	var after uint64
	if values := r.Header.Values("Last-Event-ID"); len(values) > 0 {
		if len(values) != 1 || values[0] == "" {
			return 0, errors.New("invalid Last-Event-ID")
		}
		for _, c := range values[0] {
			if c < '0' || c > '9' {
				return 0, errors.New("invalid Last-Event-ID")
			}
		}
		var err error
		after, err = strconv.ParseUint(values[0], 10, 64)
		if err != nil || after == uint64(streamhub.FromLatest) {
			return 0, errors.New("invalid Last-Event-ID")
		}
	}
	return after, nil
}

type agentCanonicalEncoder struct{ sink.Encoder }

func (e agentCanonicalEncoder) Headers() http.Header {
	headers := e.Encoder.Headers()
	headers.Set("Cache-Control", "private, no-store, no-transform")
	return headers
}

// Write serializes a previously authorized subscription. Expired logs produce
// a gap to the authoritative checkpoint; disconnects never create an outcome.
func Write(w http.ResponseWriter, r *http.Request, sub streamhub.Subscription, state State, expired bool) error {
	if sub == nil && !expired {
		return errors.New("native subscription unavailable")
	}
	encoder := agentCanonicalEncoder{native.New()}
	w.Header().Set("X-Chat-Encoding", "chatstream/v1")
	writer, err := sink.Start(w, encoder)
	if err != nil {
		if sub != nil {
			_ = sub.Close()
		}
		return err
	}
	if expired {
		gap := chatstream.Event{V: chatstream.SchemaVersion, RunID: state.RunID, Time: time.Now().UTC(), Verb: chatstream.VerbGap, Reason: chatstream.GapRetention, From: state.After + 1, To: state.Checkpoint}
		if state.After > state.Checkpoint {
			gap.Reason = chatstream.GapCursorAhead
			gap.From = state.After
			gap.To = state.Checkpoint
		}
		if state.After != state.Checkpoint {
			return encoder.Encode(writer, gap)
		}
		return nil
	}
	defer func() { _ = sub.Close() }()
	var comments sink.Bind
	for {
		nextCtx, cancel := context.WithTimeout(r.Context(), agentservice.Keepalive)
		item, nextErr := sub.Next(nextCtx)
		cancel()
		if errors.Is(nextErr, context.DeadlineExceeded) && r.Context().Err() == nil {
			if err := comments.Comment(writer, "keepalive"); err != nil {
				return err
			}
			continue
		}
		if nextErr != nil {
			if errors.Is(nextErr, io.EOF) {
				return nil
			}
			return nextErr
		}
		var ev chatstream.Event
		if item.Gap != nil {
			ev = hubbind.GapEvent(state.RunID, time.Now().UTC(), *item.Gap)
		} else {
			ev, err = hubbind.Decode(item.Record)
			if err != nil {
				return err
			}
		}
		if ev.RunID != state.RunID {
			return ErrForeignRun
		}
		if err := encoder.Encode(writer, ev); err != nil {
			return err
		}
		if ev.IsTerminal() {
			return nil
		}
	}
}
