package eventbussink

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents/ingest"
	"github.com/hollis-labs/libs/mesh/eventbus"
)

// lifecycle is a resource.state_changed event as an app would emit it.
func lifecycle(app string) runtimeevents.Event {
	return runtimeevents.Event{
		SchemaVersion: runtimeevents.SchemaVersion,
		ID:            runtimeevents.NewEventID(),
		Kind:          "resource.state_changed",
		Time:          time.Now().UTC(),
		Subject:       "msg://resource/" + app + "/example",
		Sequence:      1,
		Source:        runtimeevents.Source{Channel: "outbox", Confidence: runtimeevents.ConfidenceExact},
		Payload:       json.RawMessage(`{"entity":{"type":"resource","id":"example"},"to":"running","trigger":{"kind":"system"},"reason":"restart"}`),
	}
}

// What the adapter sends passes the hub's own per-event checks (the ingest
// contract's CheckItem), for an app that owns the kind, with App filled in.
func TestAdapterOutputPassesTheIngestCheck(t *testing.T) {
	r := &recorder{}
	if err := New(r, WithApp("cerberus")).Write(context.Background(), lifecycle("cerberus")); err != nil {
		t.Fatal(err)
	}
	if len(r.items) != 1 {
		t.Fatalf("items = %d", len(r.items))
	}
	if _, rej := ingest.CheckItem("cerberus", r.items[0].Body, ingest.Options{}); rej != nil {
		t.Fatalf("the hub would reject the adapter's output: %v", rej)
	}
}

// The gap marker, for every app that publishes, is an event the hub accepts.
func TestGapMarkerPassesTheIngestCheckForEveryApp(t *testing.T) {
	first := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	for _, app := range []string{"cerberus", "torque", "hadron", "nanite", "fragments-engine"} {
		it, err := GapMarker(app)(eventbus.Loss{Class: "protected", First: first, Last: first.Add(time.Minute), Count: 3})
		if err != nil {
			t.Fatalf("%s: %v", app, err)
		}
		if _, rej := ingest.CheckItem(app, it.Body, ingest.Options{}); rej != nil {
			t.Errorf("%s: the hub would reject the gap marker: %v", app, rej)
		}
	}
}

// fakeHub implements the hub's side with the contract's own types: ParseRequest,
// CheckItem and NewResponse.
type fakeHub struct {
	app string
	mu  sync.Mutex
	got []string
}

func (h *fakeHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	raws, err := ingest.ParseRequest(body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	results := make([]ingest.Result, 0, len(raws))
	for i, raw := range raws {
		ev, rej := ingest.CheckItem(h.app, raw, ingest.Options{})
		if rej != nil {
			results = append(results, rej.Result(ev.ID))
			continue
		}
		h.mu.Lock()
		h.got = append(h.got, ev.ID)
		h.mu.Unlock()
		results = append(results, ingest.Result{ID: ev.ID, Status: ingest.StatusAccepted, Cursor: fmt.Sprintf("%d", i+1)})
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(ingest.NewResponse(results))
}

// The whole chain: the adapter as a runtimeevents.Sink, the real client and a hub
// built from the contract. Every event is accepted, and a protected loss is
// announced with a gap marker the hub accepts, which then clears the record.
func TestEndToEndAgainstTheContractHub(t *testing.T) {
	hub := &fakeHub{app: "cerberus"}
	srv := httptest.NewServer(hub)
	defer srv.Close()
	dir := t.TempDir()

	// A process that overflowed while the hub was down leaves a loss record.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	c1, err := eventbus.New(eventbus.Config{URL: dead.URL, SpoolDir: dir, SpoolMaxBytes: 1500, QueueSize: 1, BatchSize: 1,
		FlushInterval: 10 * time.Millisecond, BackoffBase: time.Millisecond, BackoffMax: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	sink1 := New(c1, WithApp("cerberus"))
	for i := 0; i < 30; i++ {
		if err := sink1.Write(context.Background(), lifecycle("cerberus")); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = c1.Close(ctx)
	dead.Close()
	if st := c1.Stats(); len(st.Losses) != 1 {
		t.Fatalf("setup: expected a loss record, stats = %+v", st)
	}

	// The next process delivers what is left and announces the loss.
	c2, err := eventbus.New(eventbus.Config{URL: srv.URL, SpoolDir: dir, SpoolMaxBytes: 1500,
		FlushInterval: 10 * time.Millisecond, BackoffBase: time.Millisecond, BackoffMax: 5 * time.Millisecond,
		GapMarker: GapMarker("cerberus")})
	if err != nil {
		t.Fatal(err)
	}
	if err := New(c2, WithApp("cerberus")).Write(context.Background(), lifecycle("cerberus")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(c2.Stats().Losses) != 0 || c2.Stats().Sent == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the loss was never announced and cleared: %+v", c2.Stats())
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	if err := c2.Close(ctx2); err != nil {
		t.Fatal(err)
	}
	if st := c2.Stats(); st.Quarantined != 0 || st.Rejected != 0 {
		t.Fatalf("the contract hub rejected something the adapter or client sent: %+v", st)
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.got) < 2 {
		t.Fatalf("hub accepted only %d events", len(hub.got))
	}
}

// Protected-loss accounting works through the adapter: protected events are not
// downgraded by debug severity or an explicit activity class, so they are the
// last thing a full queue gives up, and giving one up is counted and recorded.
func TestProtectedEventsAreAccountedForThroughTheAdapter(t *testing.T) {
	var drops sync.Map
	c, err := eventbus.New(eventbus.Config{
		URL: "http://127.0.0.1:1", QueueSize: 3, FlushInterval: time.Hour,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnDrop: func(n int, reason string) {
			v, _ := drops.LoadOrStore(reason, new(int))
			*(v.(*int)) += n
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sink := New(c, WithApp("cerberus"))
	write := func(kind runtimeevents.EventKind, class *runtimeevents.Class) {
		ev := lifecycle("cerberus")
		ev.Kind, ev.Class = kind, class
		ev.SessionID = runtimeevents.NewSessionID() // session-bound kinds need one
		if err := sink.Write(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
	}
	// Three events fill the queue: one real activity, then two protected kinds
	// that a naive mapping would have downgraded.
	write("process.started", nil)                                           // activity
	write("task.state_changed", &runtimeevents.Class{Severity: "debug"})    // protected despite debug severity
	write("task.state_changed", &runtimeevents.Class{Category: "activity"}) // protected despite the class
	// A fourth protected event evicts the activity one: nothing protected is lost.
	write("alert.raised", nil)
	if st := c.Stats(); st.DroppedHigh != 0 || len(st.Losses) != 0 {
		t.Fatalf("the activity event must go first: %+v", st)
	}
	// A fifth finds only protected events left: that loss is counted and recorded.
	write("audit.recorded", nil)
	st := c.Stats()
	if st.DroppedHigh != 1 || len(st.Losses) != 1 || st.Losses[0].Count != 1 {
		t.Fatalf("losing a protected event must be counted and recorded: %+v", st)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.Close(ctx)
}
