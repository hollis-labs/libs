package native

import (
	"net/http"
	"time"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/sink"
)

// Option configures New.
type Option func(*encoder)

// WithClock sets the clock used to stamp the run.error Close synthesizes when
// the run had not ended. The default is time.Now.
func WithClock(now func() time.Time) Option { return func(e *encoder) { e.now = now } }

// New returns a native Encoder.
func New(opts ...Option) sink.Encoder {
	e := &encoder{now: time.Now}
	for _, o := range opts {
		o(e)
	}
	return e
}

type encoder struct {
	sink.Bind
	sink.Lifecycle
	now   func() time.Time
	runID string
}

func (e *encoder) Name() string         { return "native" }
func (e *encoder) ContentType() string  { return "text/event-stream" }
func (e *encoder) Headers() http.Header { return sink.SSEHeaders() }

func (e *encoder) Encode(w sink.Writer, ev chatstream.Event) error {
	if err := e.Admit(); err != nil {
		return err
	}
	data, err := sink.Marshal(ev)
	if err != nil {
		return err
	}
	if ev.RunID != "" {
		e.runID = ev.RunID
	}
	if err := e.Send(w, sink.FrameID(ev), string(ev.Verb), data); err != nil {
		return err
	}
	if ev.IsTerminal() {
		e.SetTerminal()
	}
	return nil
}

// Close writes nothing when the run ended in its terminal event. Otherwise it
// writes a run.error with code chatstream.CodeStreamLost, retryable, carrying
// the cause, so a native client always gets a terminal event. It has no id: it
// is not part of the hub's log.
func (e *encoder) Close(w sink.Writer, cause error) error {
	already, ended := e.Closing()
	if already || ended {
		return nil
	}
	ev := chatstream.Event{
		V: chatstream.SchemaVersion, RunID: e.runID, Time: e.now(), Verb: chatstream.VerbRunError,
		Code: chatstream.CodeStreamLost, Retryable: true, Message: sink.CauseText(cause),
	}
	data, err := sink.Marshal(ev)
	if err != nil {
		return err
	}
	return e.Send(w, "", string(ev.Verb), data)
}
