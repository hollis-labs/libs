package hubbind_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"

	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui-go/chatstream/conformance"
	"github.com/hollis-labs/libs/ui-go/chatstream/framing"
	"github.com/hollis-labs/libs/ui-go/chatstream/hubbind"
	"github.com/hollis-labs/libs/ui-go/chatstream/sink"
	"github.com/hollis-labs/libs/ui-go/chatstream/sink/native"
	ssekit "github.com/hollis-labs/libs/ui-go/ssekit"
	streamhub "github.com/hollis-labs/libs/ui-go/streamhub"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func ev(verb chatstream.Verb, f func(*chatstream.Event)) chatstream.Event {
	e := chatstream.Event{V: chatstream.SchemaVersion, RunID: "run", Time: t0, Verb: verb}
	if f != nil {
		f(&e)
	}
	return e
}

// run is a small complete run: 12 events.
func run() []chatstream.Event {
	evs := []chatstream.Event{ev(chatstream.VerbRunStart, nil),
		ev(chatstream.VerbMessageStart, func(e *chatstream.Event) { e.MessageID, e.Role = "m", "assistant" })}
	evs = append(evs, ev(chatstream.VerbPartStart, func(e *chatstream.Event) { e.PartID, e.Kind = "p", "text" }))
	for _, w := range []string{"a", "b", "c", "d", "e"} {
		evs = append(evs, ev(chatstream.VerbPartDelta, func(e *chatstream.Event) { e.PartID, e.Text = "p", w }))
	}
	return append(evs,
		ev(chatstream.VerbPartEnd, func(e *chatstream.Event) { e.PartID = "p" }),
		ev(chatstream.VerbMessageEnd, nil),
		ev(chatstream.VerbRunFinish, func(e *chatstream.Event) { e.Reason = "stop" }),
	)
}

func newHub(opts ...streamhub.Option) *streamhub.Hub {
	return streamhub.New(streamhub.NewMemoryLog(), append([]streamhub.Option{streamhub.WithTerminal(hubbind.Terminal)}, opts...)...)
}

func publishAll(t *testing.T, hub *streamhub.Hub, stream string, evs []chatstream.Event) []chatstream.Event {
	t.Helper()
	var out []chatstream.Event
	for _, e := range evs {
		got, err := hubbind.Publish(context.Background(), hub, stream, e)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, got)
	}
	return out
}

func collect(t *testing.T, sub streamhub.Subscription) []chatstream.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out []chatstream.Event
	for e, err := range hubbind.Events(ctx, sub, "run") {
		if err != nil {
			t.Fatalf("events: %v", err)
		}
		out = append(out, e)
	}
	return out
}

// The hub assigns Seq; what a subscriber receives is what was published, with
// the cursor set, and the whole thing is a valid stream.
func TestPublishedEventsComeBackWithHubSeq(t *testing.T) {
	hub := newHub()
	defer hub.Shutdown(context.Background())
	published := publishAll(t, hub, "s", run())
	for i, e := range published {
		if e.Seq != uint64(i+1) {
			t.Fatalf("event %d has Seq %d, want the hub's %d", i, e.Seq, i+1)
		}
	}
	sub, err := hub.Subscribe(context.Background(), "s", streamhub.SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	got := collect(t, sub)
	if !reflect.DeepEqual(got, published) {
		t.Fatalf("subscriber got %d events differing from what was published", len(got))
	}
	conformance.Check(t, got)
	if err := conformance.CheckReplayEquivalence(got); err != nil {
		t.Fatal(err)
	}
}

// After the terminal event the hub ends the stream: the subscriber sees EOF
// (Events ends cleanly), and publishing more is refused.
func TestTerminalEventEndsTheStream(t *testing.T) {
	hub := newHub()
	defer hub.Shutdown(context.Background())
	publishAll(t, hub, "s", run())
	if _, err := hubbind.Publish(context.Background(), hub, "s", ev(chatstream.VerbRaw, func(e *chatstream.Event) { e.Raw = &chatstream.Raw{Dialect: "d"} })); err == nil {
		t.Fatal("an event after the terminal event must be refused by the hub")
	}
}

// Resuming from any cursor yields exactly the events after it, and reducing
// them onto the snapshot at the cursor equals reducing the whole run.
func TestResumeFromAnyCursor(t *testing.T) {
	hub := newHub()
	defer hub.Shutdown(context.Background())
	all := publishAll(t, hub, "s", run())
	full, err := chatstream.Reduce(all, nil)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(full)
	for k := 0; k <= len(all); k++ {
		sub, err := hub.Subscribe(context.Background(), "s", streamhub.SubscribeOptions{After: streamhub.Seq(k)})
		if err != nil {
			t.Fatal(err)
		}
		rest := collect(t, sub)
		sub.Close()
		if len(rest) != len(all[k:]) || (len(rest) > 0 && !reflect.DeepEqual(rest, all[k:])) {
			t.Fatalf("resume after %d: got %d events, want %d", k, len(rest), len(all)-k)
		}
		snap, err := chatstream.Reduce(all[:k], nil)
		if err != nil {
			t.Fatal(err)
		}
		got, err := chatstream.Reduce(rest, snap)
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := json.Marshal(got); string(b) != string(want) {
			t.Fatalf("resume after %d reduces differently:\n%s\n%s", k, b, want)
		}
	}
}

// Retention drops the start of the stream; a subscriber that asks for it gets an
// in-band gap event, and the reduced message says it is incomplete.
func TestRetentionGapArrivesInBand(t *testing.T) {
	hub := newHub(streamhub.WithRetention(streamhub.Retention{MaxRecords: 4}))
	defer hub.Shutdown(context.Background())
	publishAll(t, hub, "s", run())
	sub, err := hub.Subscribe(context.Background(), "s", streamhub.SubscribeOptions{After: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	got := collect(t, sub)
	if got[0].Verb != chatstream.VerbGap || got[0].Reason != chatstream.GapRetention {
		t.Fatalf("first event = %+v, want a retention gap", got[0])
	}
	// 11 events, the last 4 retained: records 2..7 are the ones lost after the cursor
	if got[0].From != 2 || got[0].To != 7 {
		t.Errorf("gap covers %d..%d, want the lost records 2..7", got[0].From, got[0].To)
	}
	// what follows the gap starts mid-part; the reducer copes and says so
	m, err := chatstream.Reduce(got, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Incomplete || len(m.Gaps) != 1 || m.Status != chatstream.StatusFinished {
		t.Errorf("a gap must mark the message incomplete: %+v", m)
	}
}

func TestGapEventForEachReason(t *testing.T) {
	cases := []struct {
		g        streamhub.Gap
		reason   string
		from, to uint64
	}{
		{streamhub.Gap{Reason: streamhub.GapRetention, Requested: 3, OldestAvailable: 10}, chatstream.GapRetention, 4, 9},
		{streamhub.Gap{Reason: streamhub.GapDropped, Requested: 5, OldestAvailable: 9}, chatstream.GapDroppedSlow, 6, 8},
		{streamhub.Gap{Reason: streamhub.GapCursorAhead, Requested: 50, Latest: 12}, chatstream.GapCursorAhead, 50, 12},
	}
	for _, c := range cases {
		e := hubbind.GapEvent("r", t0, c.g)
		if e.Verb != chatstream.VerbGap || e.Reason != c.reason || e.From != c.from || e.To != c.to || e.RunID != "r" {
			t.Errorf("%+v -> %+v", c.g, e)
		}
	}
}

// A slow subscriber is closed, not silently thinned; the error tells it where
// to resume, and resuming loses nothing.
func TestSlowConsumerResumesFromLastSeq(t *testing.T) {
	hub := newHub()
	defer hub.Shutdown(context.Background())
	ctx := context.Background()
	if _, err := hubbind.Publish(ctx, hub, "s", run()[0]); err != nil {
		t.Fatal(err)
	}
	sub, err := hub.Subscribe(ctx, "s", streamhub.SubscribeOptions{After: streamhub.FromLatest, Buffer: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	all := run()
	for _, e := range all[1:] {
		if _, perr := hubbind.Publish(ctx, hub, "s", e); perr != nil {
			t.Fatal(perr)
		}
	}
	var got []chatstream.Event
	var last uint64
	for e, err := range hubbind.Events(ctx, sub, "run") {
		if err != nil {
			if !errors.Is(err, streamhub.ErrSlowConsumer) {
				t.Fatalf("err = %v", err)
			}
			break
		}
		got = append(got, e)
		last = e.Seq
	}
	resumed, err := hub.Subscribe(ctx, "s", streamhub.SubscribeOptions{After: streamhub.Seq(last)})
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	got = append(got, collect(t, resumed)...)
	// the first event was published before the subscription began (FromLatest)
	if len(got) != len(all)-1 {
		t.Fatalf("resume lost events: %d of %d", len(got), len(all)-1)
	}
	for i, e := range got {
		if e.Seq != uint64(i+2) {
			t.Fatalf("event %d has Seq %d: the resumed stream is not contiguous", i, e.Seq)
		}
	}
}

// handler serves a session over SSE: the native encoder for the frames,
// go-ssekit's ResumeCursor for the cursor (header only), the hub for replay.
func handler(hub *streamhub.Hub, stream string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var after streamhub.Seq
		if c, ok := ssekit.ResumeCursor(r); ok { // no WithQueryKeys: header only
			n, err := strconv.ParseUint(c, 10, 64)
			if err != nil {
				http.Error(w, "bad Last-Event-ID", http.StatusBadRequest)
				return
			}
			after = streamhub.Seq(n)
		}
		sub, err := hub.Subscribe(r.Context(), stream, streamhub.SubscribeOptions{After: after})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer sub.Close()
		enc := native.New()
		out, err := sink.Start(w, enc)
		if err != nil {
			return
		}
		for e, err := range hubbind.Events(r.Context(), sub, "run") {
			if err != nil || enc.Encode(out, e) != nil {
				return
			}
		}
		_ = enc.Close(out, nil)
	}
}

func readEvents(t *testing.T, url, lastEventID string, limit int) []chatstream.Event {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out []chatstream.Event
	for f, err := range framing.SSE(resp.Body) {
		if err != nil {
			t.Fatal(err)
		}
		var e chatstream.Event
		if err := json.Unmarshal(f.Data, &e); err != nil {
			t.Fatal(err)
		}
		if f.ID != "" && f.ID != itoa(e.Seq) {
			t.Fatalf("frame id %q, event seq %d", f.ID, e.Seq)
		}
		out = append(out, e)
		if limit > 0 && len(out) == limit {
			return out
		}
	}
	return out
}

func itoa(n uint64) string {
	return string(json.Number(func() string { b, _ := json.Marshal(n); return string(b) }()))
}

// The whole path: hub, native encoder, real HTTP, ssekit's resume cursor,
// framing back to events. A client that dropped after k events reconnects with
// Last-Event-ID and gets the rest, exactly.
func TestResumeOverHTTPWithLastEventID(t *testing.T) {
	hub := newHub()
	defer hub.Shutdown(context.Background())
	all := publishAll(t, hub, "s", run())
	srv := httptest.NewServer(handler(hub, "s"))
	defer srv.Close()

	first := readEvents(t, srv.URL, "", 5)
	if !reflect.DeepEqual(first, all[:5]) {
		t.Fatalf("first connection: %d events, not the first five", len(first))
	}
	rest := readEvents(t, srv.URL, itoa(first[len(first)-1].Seq), 0)
	if !reflect.DeepEqual(rest, all[5:]) {
		t.Fatalf("resume after 5: got %d events, want %d", len(rest), len(all)-5)
	}
	whole := append(first, rest...)
	conformance.Check(t, whole)
}

// The ratified contract is header-only: a cursor in the query string is not a
// cursor.
func TestResumeCursorInTheQueryStringIsIgnored(t *testing.T) {
	hub := newHub()
	defer hub.Shutdown(context.Background())
	all := publishAll(t, hub, "s", run())
	srv := httptest.NewServer(handler(hub, "s"))
	defer srv.Close()
	got := readEvents(t, srv.URL+"?last_event_id=7&since_seq=7&after=7", "", 0)
	if len(got) != len(all) {
		t.Fatalf("a query-string cursor must be ignored: got %d events, want the full %d", len(got), len(all))
	}
}
