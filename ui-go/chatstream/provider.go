package chatstream

import (
	"context"
	"encoding/json"
	"iter"
	"time"
)

// Request asks a Provider to start a run. What is in it is the provider's
// business: chatstream does not model prompts, tools or sampling, so Body is
// the dialect's own request document and Ext carries provider options. This
// module makes no HTTP calls; a Provider is whatever transports and decodes.
type Request struct {
	// RunID names the run the events will carry. A Provider generates one when
	// it is empty.
	RunID string
	Model string
	// Body is the provider's own request payload.
	Body json.RawMessage
	Ext  map[string]json.RawMessage
}

// Provider is an endpoint plus an Adapter plus its quirks: it starts runs and
// declares what their streams can do.
type Provider interface {
	Name() string
	// Capabilities is static, immutable and safe for concurrent use.
	Capabilities() Capabilities
	Stream(ctx context.Context, req Request) (Stream, error)
}

// Stream is one run's events.
type Stream interface {
	// Events yields the run's events in order and ends after exactly one
	// terminal event. A non-nil error is a failure of the stream machinery
	// itself, not an upstream error (those are run.error events); it ends the
	// sequence.
	Events() iter.Seq2[Event, error]
	// Cancel asks the upstream to stop, if Capabilities().Cancel.
	Cancel(ctx context.Context) error
	// Close releases the stream.
	Close() error
}

// Frame is one unit of an upstream stream after framing: an SSE event (Event,
// Data, ID), or one JSON line (Data only). Data is the frame's payload,
// unmodified.
type Frame struct {
	Event string
	Data  []byte
	ID    string
}

// DecodeOptions configure a Decoder for one stream.
type DecodeOptions struct {
	// RunID is stamped on every event. When empty the decoder uses the id the
	// upstream supplies, or generates none.
	RunID string
	// Provider and Model go on run.start when the upstream does not say.
	Provider string
	Model    string
	// Now is the decoder's clock, for Event.Time. Nil means time.Now.
	Now func() time.Time
}

// Adapter is one dialect: how its stream is framed and a Decoder for it.
// Adapters are separately importable (chatstream/adapter/...), so an
// application pulls in only the dialects it speaks.
type Adapter interface {
	// Name is the dialect: "anthropic.messages", "openai.chat", "acp", ...
	Name() string
	Framing() Framing
	// Capabilities are the dialect's defaults; a Provider may override them.
	Capabilities() Capabilities
	NewDecoder(DecodeOptions) Decoder
}

// Decoder turns one stream's frames into events. It owns the accumulators and
// index-to-id maps the dialect needs, and is not safe for concurrent use.
//
// The contract every decoder keeps, and the conformance kit checks:
//
//   - Events come out in lifecycle order: a part opens before its deltas and
//     closes before the run's terminal event.
//   - Exactly one terminal event ends the run, and nothing follows it.
//   - A frame that cannot be decoded is preserved as a raw event and decoding
//     continues, unless the dialect makes it fatal. It is never silently
//     dropped and is not, by itself, terminal.
//   - An upstream error event is terminal only when the dialect says so.
//   - Truncation is never success: Close on a stream that has not seen its
//     terminal signal closes what is open and emits run.error with
//     CodeUpstreamTruncated.
type Decoder interface {
	// Decode consumes one frame. The error is for a decoder that cannot
	// continue at all (ErrDecoderClosed); frame-level problems become events.
	Decode(Frame) ([]Event, error)
	// Close ends the stream. cause is why: nil for a clean upstream EOF, an
	// error for a broken connection or a canceled context. It returns the
	// events that make the stream well formed (open parts closed, then one
	// terminal event) or nil if the run already ended. Close is idempotent.
	Close(cause error) []Event
}
