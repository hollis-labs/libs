package sink

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
	ssekit "github.com/hollis-labs/libs/ui-go/ssekit"
)

// Writer is where an Encoder writes: bytes plus an explicit flush whose error
// is the caller's to see. Start wraps an http.ResponseWriter into one.
type Writer interface {
	io.Writer
	Flush() error
}

// Encoder writes one run's events for one target. See the package comment for
// the contract every implementation keeps.
type Encoder interface {
	Name() string
	ContentType() string
	// Headers are the response headers the target needs, Content-Type included.
	// Start applies them.
	Headers() http.Header
	// Encode writes the frames ev maps to (possibly none).
	Encode(w Writer, ev chatstream.Event) error
	// Close ends the stream. cause is why it ended: nil when the run ended in
	// its terminal event, otherwise the reason the stream is being cut short.
	Close(w Writer, cause error) error
}

// Errors returned by encoders.
var (
	// ErrClosed is returned by Encode after Close.
	ErrClosed = errors.New("sink: encoder is closed")
	// ErrWriterChanged is returned when an Encoder is handed a different Writer
	// than the one it started on.
	ErrWriterChanged = errors.New("sink: encoder was given a different Writer")
	// ErrOutOfOrder is returned by Encode for an event that does not follow from
	// the events the encoder has seen: a part.delta or part.end for a part that is
	// not open, a part.start for an id that is already open, a step.finish for a
	// step that is not open. The usual cause is an encoder fed the tail of a run
	// (a resumed subscription) instead of the run from its first event. The event
	// is refused, nothing is written for it and the encoder's state is unchanged.
	ErrOutOfOrder = errors.New("sink: event out of order")
)

// OutOfOrder is the error Encode returns for an event that does not follow from
// what the encoder has seen; it wraps ErrOutOfOrder.
func OutOfOrder(ev chatstream.Event, why string) error {
	id := ev.PartID
	if id == "" {
		id = ev.StepID
	}
	return fmt.Errorf("%w: %s %q: %s", ErrOutOfOrder, ev.Verb, id, why)
}

// SSEHeaders returns the headers an SSE response needs, the same defaults as
// go-ssekit's NewWriter. Encoders start from these and add their own.
func SSEHeaders() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	return h
}

// Start prepares w for enc: it sets enc.Headers(), clears the connection's
// write and read deadlines (best effort), sends status 200 and flushes so the
// client sees the response immediately. It returns an error, before touching
// the response, if w cannot flush.
func Start(w http.ResponseWriter, enc Encoder) (Writer, error) {
	if !canFlush(w) {
		return nil, ssekit.ErrNoFlusher
	}
	rc := http.NewResponseController(w)
	h := w.Header()
	for k, vs := range enc.Headers() {
		h[k] = append([]string(nil), vs...)
	}
	_ = rc.SetWriteDeadline(time.Time{})
	_ = rc.SetReadDeadline(time.Time{})
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		return nil, err
	}
	return &responseWriter{w: w, rc: rc}, nil
}

func canFlush(w http.ResponseWriter) bool {
	for {
		switch v := w.(type) {
		case interface{ FlushError() error }:
			return true
		case http.Flusher:
			return true
		case interface{ Unwrap() http.ResponseWriter }:
			w = v.Unwrap()
		default:
			return false
		}
	}
}

type responseWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func (r *responseWriter) Write(p []byte) (int, error) { return r.w.Write(p) }
func (r *responseWriter) Flush() error                { return r.rc.Flush() }

// Bind frames SSE onto a Writer with go-ssekit. An Encoder embeds one, binds it
// to the first Writer it sees and reuses it, so ssekit's sticky error and
// per-frame flush apply for the encoder's whole life.
type Bind struct {
	w   Writer
	sse *ssekit.Writer
}

// Send writes one frame: id (omitted when empty), event name (omitted when
// empty) and data, then flushes.
func (b *Bind) Send(w Writer, id, event string, data []byte) error {
	if err := b.bind(w); err != nil {
		return err
	}
	return b.sse.Send(ssekit.Event{ID: id, Name: event, Data: data})
}

// Comment writes an SSE comment line, which clients ignore.
func (b *Bind) Comment(w Writer, text string) error {
	if err := b.bind(w); err != nil {
		return err
	}
	return b.sse.Comment(text)
}

func (b *Bind) bind(w Writer) error {
	if b.sse != nil {
		if !sameWriter(w, b.w) {
			return ErrWriterChanged
		}
		return nil
	}
	sse, err := ssekit.NewWriter(&adapter{w: w, h: http.Header{}})
	if err != nil {
		return fmt.Errorf("sink: start SSE: %w", err)
	}
	b.w, b.sse = w, sse
	return nil
}

// sameWriter compares two Writers; a Writer whose dynamic type is not
// comparable is taken to be the same one rather than panicking.
func sameWriter(a, b Writer) (same bool) {
	defer func() {
		if recover() != nil {
			same = true
		}
	}()
	return a == b
}

// adapter presents a sink.Writer as the http.ResponseWriter ssekit.NewWriter
// wants. Headers and status are scratch: the real response was prepared by
// Start, so only the bytes and the flushes reach the Writer.
type adapter struct {
	w Writer
	h http.Header
}

func (a *adapter) Header() http.Header         { return a.h }
func (a *adapter) WriteHeader(int)             {}
func (a *adapter) Write(p []byte) (int, error) { return a.w.Write(p) }
func (a *adapter) FlushError() error           { return a.w.Flush() }

// Lifecycle is the terminal and closed bookkeeping every encoder shares.
type Lifecycle struct {
	terminal bool
	closed   bool
}

// Admit is called at the top of Encode: it returns ErrClosed after Close, an
// error wrapping chatstream.ErrAfterTerminal after the terminal event, and nil
// otherwise.
func (l *Lifecycle) Admit() error {
	if l.closed {
		return ErrClosed
	}
	if l.terminal {
		return fmt.Errorf("sink: %w", chatstream.ErrAfterTerminal)
	}
	return nil
}

// SetTerminal records that the terminal event was encoded.
func (l *Lifecycle) SetTerminal() { l.terminal = true }

// Terminated reports whether the terminal event was encoded.
func (l *Lifecycle) Terminated() bool { return l.terminal }

// Closing is called at the top of Close: it reports whether Close already ran
// (and marks it now), and whether the run had ended.
func (l *Lifecycle) Closing() (already, ended bool) {
	if l.closed {
		return true, l.terminal
	}
	l.closed = true
	return false, l.terminal
}

// CauseText is the human message for a stream cut short: the cause if given,
// else a generic sentence.
func CauseText(cause error) string {
	if cause != nil {
		return "the stream ended before the run finished: " + cause.Error()
	}
	return "the stream ended before the run finished"
}

// FrameID is the SSE id for an event: its Seq when assigned, "" otherwise.
func FrameID(ev chatstream.Event) string {
	if ev.Seq == 0 {
		return ""
	}
	return fmt.Sprintf("%d", ev.Seq)
}

// Milliseconds is t as milliseconds since the Unix epoch, 0 for the zero time.
func Milliseconds(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// Conventions the encoders share for information the core Event has no field
// for; they are defined in the chatstream package (meta.go) and aliased here.
const (
	MetaName      = chatstream.MetaName
	MetaCallID    = chatstream.MetaCallID
	MetaIsError   = chatstream.MetaIsError
	MetaDetail    = chatstream.MetaDetail
	MetaPhase     = chatstream.MetaPhase
	MetaURL       = chatstream.MetaURL
	MetaMediaType = chatstream.MetaMediaType
	MetaTitle     = chatstream.MetaTitle
	MetaFilename  = chatstream.MetaFilename
	MetaSourceID  = chatstream.MetaSourceID
	// MetaDataName names a data part ("weather" becomes data-weather).
	MetaDataName = chatstream.MetaName

	ActivityReplaceContent = chatstream.ActivityReplaceContent
	ActivityStateSnapshot  = chatstream.ActivityStateSnapshot
	ActivityStateDelta     = chatstream.ActivityStateDelta
)

// MetaString returns the string value of key in ev.Meta ("" when absent or not a
// JSON string).
func MetaString(ev chatstream.Event, key string) string {
	raw, ok := ev.Meta[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// MetaBool returns the boolean value of key in ev.Meta (false when absent).
func MetaBool(ev chatstream.Event, key string) bool {
	raw, ok := ev.Meta[key]
	if !ok {
		return false
	}
	var b bool
	return json.Unmarshal(raw, &b) == nil && b
}

// Marshal is json.Marshal without HTML escaping (<, > and & stay as they are, as
// JavaScript's JSON.stringify writes them) and without a trailing newline. The
// encoders use it for every frame's data.
func Marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}
