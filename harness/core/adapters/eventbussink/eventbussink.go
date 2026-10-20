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
// fill it in when the producer left it empty. Before publishing, the event is
// checked against the same envelope rules the hub applies on shape (a canonical
// evt_ id, a supported schema version, an app, the required fields), so a
// producer learns about an event the hub would reject at the call, not later in
// quarantine. Which kinds an app may publish stays the hub's decision. An event
// whose class.visibility is "local" is never published: it must not leave the
// process, so it is skipped without error and counted in [Sink.Stats].
package eventbussink

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents/catalog"
	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents/ingest"
	"github.com/hollis-labs/libs/mesh/eventbus"
)

// Sink implements [runtimeevents.Sink] on top of an [eventbus.Publisher].
type Sink struct {
	pub     eventbus.Publisher
	app     string
	skipped atomic.Uint64
}

// Stats are the sink's counters.
type Stats struct {
	// SkippedLocal counts events not published because their class.visibility
	// is "local". Only the count is kept, never the event.
	SkippedLocal uint64
}

// Stats returns the counters.
func (s *Sink) Stats() Stats { return Stats{SkippedLocal: s.skipped.Load()} }

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

// Write checks ev, strips the fields the hub owns and hands it to the
// publisher. It returns an error only if ev is not an envelope the hub would
// accept on shape, which the producer can act on; delivery problems never reach
// the caller. An event with class.visibility "local" is skipped without error.
func (s *Sink) Write(_ context.Context, ev runtimeevents.Event) error {
	if ev.Class != nil && ev.Class.Visibility == "local" {
		s.skipped.Add(1)
		return nil // local means it never leaves the process
	}
	ev.Cursor = ""
	ev.RecordedAt = nil
	ev.Actor = nil
	if ev.App == "" {
		ev.App = s.app
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("eventbussink: marshal event %s: %w", ev.ID, err)
	}
	if _, rej := ingest.CheckEnvelope(body); rej != nil {
		return fmt.Errorf("eventbussink: event %s: %w", ev.ID, rej)
	}
	s.pub.Publish(eventbus.Item{ID: ev.ID, Priority: priority(ev), Body: body})
	return nil
}

// protectedCategories are never downgraded: losing one is recorded and
// announced. They are the categories whose events are state, approvals, audit
// records and alerts.
var protectedCategories = map[string]bool{"state": true, "approval": true, "audit": true, "alert": true}

// priority maps an event to the client's drop order. An event is protected if
// its own class or the catalog puts it in a protected category, whatever its
// severity or an explicit class says: a state change logged at debug severity is
// still a state change. Otherwise debug severity is PriorityDebug and the
// activity category is PriorityActivity. A kind the catalog does not know is
// activity unless its class names a category, which then decides; every other
// known category is protected.
func priority(ev runtimeevents.Event) eventbus.Priority {
	var cat, sev string
	if ev.Class != nil {
		cat, sev = ev.Class.Category, ev.Class.Severity
	}
	catalogCat := ""
	known := false
	if e, ok := catalog.Lookup(string(ev.Kind)); ok {
		catalogCat, known = e.Category, true
	}
	if protectedCategories[cat] || protectedCategories[catalogCat] {
		return eventbus.PriorityHigh
	}
	if sev == "debug" {
		return eventbus.PriorityDebug
	}
	if cat == "" {
		if !known {
			return eventbus.PriorityActivity
		}
		cat = catalogCat
	}
	if cat == "activity" {
		return eventbus.PriorityActivity
	}
	return eventbus.PriorityHigh
}

// A gap marker is identified within its producer by (app, channel "eventbus",
// generation, sequence), as the contract asks of every producer: sequence only
// goes up within a generation, and the generation changes on every restart.
// Here the generation is the process start time in nanoseconds, so it also grows
// across restarts (unless the machine's clock is set back), and the sequence is
// a counter of markers built by this process.
var (
	markerGeneration = uint64(time.Now().UnixNano())
	markerSequence   atomic.Uint64
)

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
			Generation:    markerGeneration,
			Sequence:      markerSequence.Add(1),
			Source:        runtimeevents.Source{Channel: "eventbus", Confidence: runtimeevents.ConfidenceExact},
			Class:         &runtimeevents.Class{Category: "alert", Severity: "warning", PayloadSchema: "alert@1"},
			Payload:       payload,
		}
		body, err := json.Marshal(ev)
		if err != nil {
			return eventbus.Item{}, err
		}
		if _, rej := ingest.CheckEnvelope(body); rej != nil {
			return eventbus.Item{}, fmt.Errorf("eventbussink: gap marker for app %q: %w", app, rej)
		}
		return eventbus.Item{ID: ev.ID, Priority: eventbus.PriorityHigh, Body: body}, nil
	}
}
