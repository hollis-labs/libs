// Package eventbussink adapts the hub client in mesh/eventbus to
// runtimeevents.Sink, so a producer that already writes runtimeevents to a Sink
// (a FileSink, a MultiSink) can also ship them to the event hub:
//
//	pub, _ := eventbus.FromEnv("cerberus", eventbus.Config{
//		GapMarker: eventbussink.GapMarker("cerberus"),
//	})
//	sink := runtimeevents.MultiSink{localSink, eventbussink.New(pub, eventbussink.WithApp("cerberus"))}
//
// The wire body is the runtimeevents v1.1 envelope as JSON. Fields the hub owns
// (cursor, recorded_at, actor) are cleared before sending. The hub binds a
// credential to one app, so every event must carry that app: use [WithApp] to
// fill it in when the producer left it empty.
package eventbussink

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents/catalog"
	"github.com/hollis-labs/libs/mesh/eventbus"
)

// Sink implements [runtimeevents.Sink] on top of an [eventbus.Publisher].
type Sink struct {
	pub eventbus.Publisher
	app string
}

var _ runtimeevents.Sink = (*Sink)(nil)

// Option configures a [Sink].
type Option func(*Sink)

// WithApp sets Event.App on every event that does not carry one.
func WithApp(app string) Option { return func(s *Sink) { s.app = app } }

// New wraps pub. An [eventbus.Noop] publisher makes the sink a no-op.
func New(pub eventbus.Publisher, opts ...Option) *Sink {
	s := &Sink{pub: pub}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Write validates ev, strips the fields the hub owns and hands it to the
// publisher. It returns an error only if ev is not a valid envelope or cannot
// be marshalled, which the producer can act on; delivery problems never reach
// the caller.
func (s *Sink) Write(_ context.Context, ev runtimeevents.Event) error {
	ev.Cursor = ""
	ev.RecordedAt = nil
	ev.Actor = nil
	if ev.App == "" {
		ev.App = s.app
	}
	if err := ev.Validate(); err != nil {
		return fmt.Errorf("eventbussink: event %s: %w", ev.ID, err)
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("eventbussink: marshal event %s: %w", ev.ID, err)
	}
	s.pub.Publish(eventbus.Item{ID: ev.ID, Priority: priority(ev), Body: body})
	return nil
}

// priority maps an event to the client's drop order: debug severity is
// PriorityDebug, the activity category is PriorityActivity, and everything else
// (state, approval, audit, alert, unknown kinds) is protected. The event's own
// class wins over the catalog's.
func priority(ev runtimeevents.Event) eventbus.Priority {
	cat := ""
	if ev.Class != nil {
		cat = ev.Class.Category
		if ev.Class.Severity == "debug" {
			return eventbus.PriorityDebug
		}
	}
	if cat == "" {
		if e, ok := catalog.Lookup(string(ev.Kind)); ok {
			cat = e.Category
		}
	}
	if cat == "activity" {
		return eventbus.PriorityActivity
	}
	return eventbus.PriorityHigh
}

// GapMarker returns the function for eventbus.Config.GapMarker: it builds the
// alert.raised event that announces dropped protected events to the hub. The
// event has rule_id eventbus.loss, a coalesce key per app, no source events (the
// dropped ones never reached the hub) and a summary with the count and the time
// span, nothing else about them.
func GapMarker(app string) func(eventbus.Loss) (eventbus.Item, error) {
	return func(l eventbus.Loss) (eventbus.Item, error) {
		payload, err := json.Marshal(map[string]any{
			"rule_id":          "eventbus.loss",
			"source_event_ids": []string{},
			"labels":           []string{"eventbus", "loss", l.Class},
			"coalesce_key":     "eventbus.loss:" + app,
			"confidence":       "exact",
			"summary": fmt.Sprintf("%d %s events were dropped by the %s event client between %s and %s",
				l.Count, l.Class, app, l.First.UTC().Format(time.RFC3339), l.Last.UTC().Format(time.RFC3339)),
		})
		if err != nil {
			return eventbus.Item{}, err
		}
		ev := runtimeevents.Event{
			SchemaVersion: runtimeevents.SchemaVersion,
			ID:            runtimeevents.NewEventID(),
			Kind:          catalog.KindAlertRaised,
			Time:          time.Now().UTC(),
			App:           app,
			Subject:       "msg://service/" + app + "/eventbus",
			Sequence:      uint64(l.Last.UnixNano()),
			Source:        runtimeevents.Source{Channel: "eventbus", Confidence: runtimeevents.ConfidenceExact},
			Class:         &runtimeevents.Class{Category: "alert", Severity: "warning", PayloadSchema: "alert@1"},
			Payload:       payload,
		}
		if err := ev.Validate(); err != nil {
			return eventbus.Item{}, err
		}
		body, err := json.Marshal(ev)
		if err != nil {
			return eventbus.Item{}, err
		}
		return eventbus.Item{ID: ev.ID, Priority: eventbus.PriorityHigh, Body: body}, nil
	}
}
