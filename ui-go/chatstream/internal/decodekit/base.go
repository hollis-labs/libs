package decodekit

import (
	"encoding/json"
	"fmt"
	"time"

	chatstream "github.com/hollis-labs/go-chatstream"
)

// Base tracks one stream's lifecycle. It is not safe for concurrent use.
type Base struct {
	opts       chatstream.DecodeOptions
	runID      string
	started    bool
	terminated bool
	closed     bool
	openParts  []string // part ids in open order
	msgOpen    bool
	msgID      string
	openSteps  []string
}

// New returns a Base for one stream.
func New(opts chatstream.DecodeOptions) *Base {
	return &Base{opts: opts, runID: opts.RunID}
}

// Options returns the DecodeOptions the Base was built with.
func (b *Base) Options() chatstream.DecodeOptions { return b.opts }

// RunID is the id stamped on events: DecodeOptions.RunID, or the one SetRunID
// learned from the upstream.
func (b *Base) RunID() string { return b.runID }

// SetRunID adopts the upstream's run id when DecodeOptions did not supply one.
// It returns the id in effect.
func (b *Base) SetRunID(id string) string {
	if b.runID == "" {
		b.runID = id
	}
	return b.runID
}

// Now is the decoder's clock.
func (b *Base) Now() time.Time {
	if b.opts.Now != nil {
		return b.opts.Now()
	}
	return time.Now()
}

// Started reports whether a run.start was emitted; Terminated whether the
// terminal event was; Closed whether Close ran.
func (b *Base) Started() bool    { return b.started }
func (b *Base) Terminated() bool { return b.terminated }
func (b *Base) Closed() bool     { return b.closed }

// Event returns an event of verb stamped with the schema version, run id and time.
func (b *Base) Event(verb chatstream.Verb) chatstream.Event {
	return chatstream.Event{V: chatstream.SchemaVersion, RunID: b.runID, Time: b.Now(), Verb: verb}
}

// Emit appends ev to out after recording its effect on the lifecycle, and
// returns the extended slice. After the terminal event it appends nothing: a
// decoder cannot emit past the end of a run. ev is stamped if it was built by
// hand and lacks the schema version, run id or time.
func (b *Base) Emit(out []chatstream.Event, ev chatstream.Event) []chatstream.Event {
	if b.terminated {
		return out
	}
	if ev.V == "" {
		ev.V = chatstream.SchemaVersion
	}
	if ev.RunID == "" {
		ev.RunID = b.runID
	}
	if ev.Time.IsZero() {
		ev.Time = b.Now()
	}
	b.track(ev)
	return append(out, ev)
}

func (b *Base) track(ev chatstream.Event) {
	switch ev.Verb { //nolint:exhaustive // only the verbs that change what is open
	case chatstream.VerbRunStart:
		b.started = true
	case chatstream.VerbPartStart:
		b.openParts = append(b.openParts, ev.PartID)
	case chatstream.VerbPartEnd:
		b.openParts = removeLast(b.openParts, ev.PartID)
	case chatstream.VerbMessageStart:
		b.msgOpen, b.msgID = true, ev.MessageID
	case chatstream.VerbMessageEnd:
		b.msgOpen = false
	case chatstream.VerbStepStart:
		b.openSteps = append(b.openSteps, ev.StepID)
	case chatstream.VerbStepFinish:
		b.openSteps = removeLast(b.openSteps, ev.StepID)
	}
	if ev.Verb.Terminal() {
		b.terminated = true
	}
}

func removeLast(ids []string, id string) []string {
	for i := len(ids) - 1; i >= 0; i-- {
		if ids[i] == id {
			return append(ids[:i], ids[i+1:]...)
		}
	}
	return ids
}

// OpenParts returns the ids of parts opened and not yet closed, oldest first.
func (b *Base) OpenParts() []string { return append([]string(nil), b.openParts...) }

// PartOpen reports whether a part is open.
func (b *Base) PartOpen(id string) bool {
	for _, p := range b.openParts {
		if p == id {
			return true
		}
	}
	return false
}

// EnsureStarted emits a run.start (with the options' provider and model) if none
// was emitted yet. Dialects whose first frame is not a start marker call it
// before their first content.
func (b *Base) EnsureStarted(out []chatstream.Event) []chatstream.Event {
	if b.started || b.terminated {
		return out
	}
	ev := b.Event(chatstream.VerbRunStart)
	ev.Provider, ev.Model = b.opts.Provider, b.opts.Model
	return b.Emit(out, ev)
}

// Unwind appends the events that close everything still open, innermost first:
// part.end for each open part (newest first), message.end, step.finish. It
// does not emit a terminal event.
func (b *Base) Unwind(out []chatstream.Event) []chatstream.Event {
	for len(b.openParts) > 0 {
		id := b.openParts[len(b.openParts)-1]
		ev := b.Event(chatstream.VerbPartEnd)
		ev.PartID = id
		out = b.Emit(out, ev)
	}
	if b.msgOpen {
		ev := b.Event(chatstream.VerbMessageEnd)
		ev.MessageID = b.msgID
		out = b.Emit(out, ev)
	}
	for len(b.openSteps) > 0 {
		ev := b.Event(chatstream.VerbStepFinish)
		ev.StepID = b.openSteps[len(b.openSteps)-1]
		out = b.Emit(out, ev)
	}
	return out
}

// Truncated builds the events that end a stream whose upstream stopped without
// a terminal signal: a run.start if none was sent (a run must be bracketed),
// everything open closed, then run.error with CodeUpstreamTruncated. cause is
// why the upstream stopped; nil is a clean EOF. It is never a success.
func (b *Base) Truncated(cause error) []chatstream.Event {
	var out []chatstream.Event
	out = b.EnsureStarted(out)
	out = b.Unwind(out)
	msg := "upstream ended without a terminal event"
	if cause != nil {
		msg = fmt.Sprintf("upstream ended without a terminal event: %v", cause)
	}
	ev := b.Event(chatstream.VerbRunError)
	ev.Code, ev.Retryable, ev.Message = chatstream.CodeUpstreamTruncated, true, msg
	return b.Emit(out, ev)
}

// CheckOpen returns chatstream.ErrDecoderClosed once Close has run. A decoder's
// Decode calls it first.
func (b *Base) CheckOpen() error {
	if b.closed {
		return chatstream.ErrDecoderClosed
	}
	return nil
}

// Close implements the shared part of Decoder.Close: nothing if the run already
// ended (or Close already ran), else the truncation events.
func (b *Base) Close(cause error) []chatstream.Event {
	if b.closed {
		return nil
	}
	b.closed = true
	if b.terminated {
		return nil
	}
	return b.Truncated(cause)
}

// RawEvent builds a raw event preserving an upstream frame the dialect could not
// or need not map.
func (b *Base) RawEvent(dialect, typ string, payload []byte) chatstream.Event {
	ev := b.Event(chatstream.VerbRaw)
	// Raw.Payload is a json.RawMessage, so a frame that is not valid JSON (the
	// very frames that end up here) is preserved as a JSON string of its bytes:
	// an event that cannot be marshaled would break every consumer.
	body := append([]byte(nil), payload...)
	if !json.Valid(body) {
		body, _ = json.Marshal(string(payload))
	}
	ev.Raw = &chatstream.Raw{Dialect: dialect, Type: typ, Payload: json.RawMessage(body)}
	return ev
}
