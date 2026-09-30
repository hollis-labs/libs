package chatstream

import (
	"context"
	"iter"
)

// DecodeFrames runs a Decoder over a sequence of frames and yields the events.
// It is the glue between a framing layer (SSE, NDJSON lines) and the rest of
// the pipeline, and it keeps the contract a consumer relies on: the sequence
// ends after exactly one terminal event, whatever happens upstream.
//
//   - If frames ends cleanly, the decoder is closed with a nil cause, which
//     yields a truncation error if the dialect never sent its terminal signal.
//   - If frames yields an error, or ctx is canceled, the decoder is closed
//     with that cause.
//   - After the terminal event no further frame is read.
//
// A Decode error (ErrDecoderClosed is the only one a well-behaved decoder
// returns) ends the sequence with it. If the consumer stops early the decoder
// is closed and its closing events are discarded.
func DecodeFrames(ctx context.Context, dec Decoder, frames iter.Seq2[Frame, error]) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		finish := func(cause error) {
			for _, ev := range dec.Close(cause) {
				if !yield(ev, nil) {
					return
				}
			}
		}
		next, stop := iter.Pull2(frames)
		defer stop()
		for {
			if err := ctx.Err(); err != nil {
				finish(err)
				return
			}
			f, ferr, ok := next()
			if !ok {
				finish(nil)
				return
			}
			if ferr != nil {
				finish(ferr)
				return
			}
			evs, err := dec.Decode(f)
			if err != nil {
				yield(Event{}, err)
				return
			}
			for _, ev := range evs {
				if !yield(ev, nil) {
					dec.Close(nil)
					return
				}
				if ev.IsTerminal() {
					dec.Close(nil)
					return
				}
			}
		}
	}
}
