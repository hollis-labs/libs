package streamhub_test

// Scenarios ported from Tether's events/bus_test.go and bus_integration_test.go
// (Bus over a Persister), re-expressed against Hub. Tether's single global
// cursor is ONE stream ("events") whose scope/session/kind live in Name and
// Data and are selected by a per-subscription Filter.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	streamhub "github.com/hollis-labs/go-streamhub"
)

const bus = "events"

type tetherEvent struct {
	SessionID string `json:"session_id"`
	Payload   string `json:"payload,omitempty"`
}

func tetherPublish(t testing.TB, h *streamhub.Hub, kind, session, payload string) streamhub.Record {
	t.Helper()
	data, err := json.Marshal(tetherEvent{SessionID: session, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := h.Publish(context.Background(), bus, streamhub.Event{Name: kind, Data: data})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	return rec
}

// PublishSubscribe_Roundtrip
func TestTether_PublishSubscribeRoundtrip(t *testing.T) {
	h := newTestHub(t)
	sub := mustSubscribe(t, h, bus, streamhub.SubscribeOptions{})
	defer sub.Close()
	tetherPublish(t, h, "session.started", "s1", `{"to":"running"}`)
	it := mustNext(t, sub)
	var ev tetherEvent
	if err := json.Unmarshal(it.Record.Data, &ev); err != nil {
		t.Fatal(err)
	}
	if it.Record.Seq != 1 || it.Record.Name != "session.started" || ev.SessionID != "s1" || ev.Payload != `{"to":"running"}` || it.Record.At.IsZero() {
		t.Fatalf("item = %+v / %+v", it.Record, ev)
	}
}

// Filter_ScopeAllowList and Filter_SessionID: the subscription's Filter
// selects on Name (scope/kind) and on data (session).
func TestTether_Filters(t *testing.T) {
	kind := func(names ...string) func(streamhub.Record) bool {
		return func(r streamhub.Record) bool {
			for _, n := range names {
				if r.Name == n {
					return true
				}
			}
			return false
		}
	}
	session := func(id string) func(streamhub.Record) bool {
		return func(r streamhub.Record) bool {
			var ev tetherEvent
			return json.Unmarshal(r.Data, &ev) == nil && ev.SessionID == id
		}
	}
	tests := []struct {
		name   string
		filter func(streamhub.Record) bool
		want   []streamhub.Seq
	}{
		{"none", nil, []streamhub.Seq{1, 2, 3, 4}},
		{"scope allow-list", kind("session.started", "daemon.started"), []streamhub.Seq{1, 2, 4}},
		{"single kind", kind("daemon.started"), []streamhub.Seq{2}},
		{"session id", session("s2"), []streamhub.Seq{3, 4}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHub(t)
			sub := mustSubscribe(t, h, bus, streamhub.SubscribeOptions{Filter: tc.filter, After: streamhub.FromLatest})
			defer sub.Close()
			tetherPublish(t, h, "session.started", "s1", "")
			tetherPublish(t, h, "daemon.started", "", "")
			tetherPublish(t, h, "tool.call", "s2", "")
			tetherPublish(t, h, "session.started", "s2", "")
			for _, want := range tc.want {
				if got := mustNext(t, sub).Record.Seq; got != want {
					t.Fatalf("got seq %d, want %d", got, want)
				}
			}
			// Nothing else may be queued behind the filter.
			ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
			defer cancel()
			if it, err := sub.Next(ctx); err == nil {
				t.Fatalf("unexpected extra item %+v", it)
			}
		})
	}
}

// Publish_RejectsEmptyScope: the hub's required routing field is the stream
// name; an empty one is rejected. (Tether's Scope is application data.)
func TestTether_PublishRejectsEmptyStreamName(t *testing.T) {
	h := newTestHub(t)
	if _, err := h.Publish(context.Background(), "", streamhub.Event{Name: "k"}); !errors.Is(err, streamhub.ErrUnknownStream) {
		t.Fatalf("err = %v", err)
	}
}

// Publish_RejectsNilPersister: a hub cannot be built without a Log.
func TestTether_NewRejectsNilLog(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New(nil) did not panic")
		}
	}()
	streamhub.New(nil)
}

// Cancel_ClosesOutChannel: Close (the cancel func) ends the subscription,
// idempotently; canceling the subscribe context does too.
func TestTether_CancelEndsSubscription(t *testing.T) {
	ctx := context.Background()
	h := newTestHub(t)
	sub := mustSubscribe(t, h, bus, streamhub.SubscribeOptions{})
	if err := sub.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sub.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	if _, err := sub.Next(ctx); !errors.Is(err, streamhub.ErrClosed) {
		t.Fatalf("Next after Close = %v", err)
	}

	cctx, cancel := context.WithCancel(ctx)
	sub2, err := h.Subscribe(cctx, bus, streamhub.SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := sub2.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Next after ctx cancel = %v", err)
	}
	tetherPublish(t, h, "still.works", "", "")
}

// ConcurrentPublishSubscribeCancel: 200 publishers, 10 subscribers coming and
// going. Must be race-free and deadlock-free.
func TestTether_ConcurrentPublishSubscribeCancel(t *testing.T) {
	ctx := context.Background()
	h := newTestHub(t)
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sub, err := h.Subscribe(ctx, bus, streamhub.SubscribeOptions{Buffer: 1024})
			if err != nil {
				t.Errorf("Subscribe: %v", err)
				return
			}
			for range 20 {
				nctx, cancel := context.WithTimeout(ctx, 5*time.Millisecond)
				_, err := sub.Next(nctx)
				cancel()
				if err != nil {
					break
				}
			}
			_ = sub.Close()
		}()
	}
	for range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.Publish(ctx, bus, streamhub.Event{Name: "k"}); err != nil {
				t.Errorf("Publish: %v", err)
			}
		}()
	}
	wg.Wait()
}

// SinceSeq_NoBoundaryGap: publishers racing the subscribe-with-replay must
// not drop or duplicate a record at the replay/live boundary.
func TestTether_NoBoundaryGap(t *testing.T) {
	ctx := context.Background()
	h := newTestHub(t)
	for range 5 {
		tetherPublish(t, h, "seed", "s1", "")
	}
	const newPubs = 200
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range newPubs {
			if _, err := h.Publish(ctx, bus, streamhub.Event{Name: "live"}); err != nil {
				t.Errorf("Publish: %v", err)
				return
			}
		}
	}()
	sub := mustSubscribe(t, h, bus, streamhub.SubscribeOptions{Buffer: 4096})
	defer sub.Close()
	wg.Wait()
	seen := make(map[streamhub.Seq]bool)
	for want := streamhub.Seq(1); want <= 5+newPubs; want++ {
		it := mustNext(t, sub)
		if it.Gap != nil || seen[it.Record.Seq] || it.Record.Seq != want {
			t.Fatalf("got %+v, want seq %d (duplicate=%v)", it, want, seen[it.Record.Seq])
		}
		seen[it.Record.Seq] = true
	}
}

// SinceSeq_ReplaysHistoryThenLive
func TestTether_ReplaysHistoryThenLive(t *testing.T) {
	h := newTestHub(t)
	for range 5 {
		tetherPublish(t, h, "historical", "s1", "")
	}
	sub := mustSubscribe(t, h, bus, streamhub.SubscribeOptions{After: 2})
	defer sub.Close()
	for _, want := range []streamhub.Seq{3, 4, 5} {
		it := mustNext(t, sub)
		if it.Record.Seq != want || it.Record.Name != "historical" {
			t.Fatalf("replay = %+v, want seq %d", it.Record, want)
		}
	}
	tetherPublish(t, h, "live", "s1", "")
	if it := mustNext(t, sub); it.Record.Seq != 6 || it.Record.Name != "live" {
		t.Fatalf("live = %+v", it.Record)
	}
}

// SlowSubscriber_DoesNotBlockPublisher: buffer 2, never read, 100 publishes
// finish, and drops are counted. (Tether's DropOldest is DropOldest.)
func TestTether_SlowSubscriberDoesNotBlockPublisher(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := streamhub.New(streamhub.NewMemoryLog())
		sub := mustSubscribe(t, h, bus, streamhub.SubscribeOptions{Buffer: 2, Policy: streamhub.DropOldest})
		done := false
		go func() {
			for range 100 {
				tetherPublish(t, h, "k", "s1", "")
			}
			done = true
		}()
		synctest.Wait()
		if !done {
			t.Fatal("Publish blocked on a slow subscriber")
		}
		if sub.Drops() != 98 {
			t.Fatalf("Drops = %d, want 98", sub.Drops())
		}
		_ = sub.Close()
		_ = h.Shutdown(context.Background())
	})
}

// SlowSubscriber_EvictedAfterMaxConsecDrops: EvictAfterN(3), buffer 1.
func TestTether_SlowSubscriberEvictedAfterMaxConsecDrops(t *testing.T) {
	h := newTestHub(t)
	sub := mustSubscribe(t, h, bus, streamhub.SubscribeOptions{Buffer: 1, Policy: streamhub.EvictAfterN(3)})
	defer sub.Close()
	for range 50 {
		tetherPublish(t, h, "k", "", "")
	}
	// Buffer 1: record 1 fits; 2, 3 and 4 are three consecutive drops, so
	// the subscription is evicted at record 4 and never sees the rest.
	var sawEvict bool
	for range 10 {
		_, err := sub.Next(context.Background())
		if errors.Is(err, streamhub.ErrSlowConsumer) {
			sawEvict = true
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
	}
	if !sawEvict {
		t.Fatal("subscription was not evicted")
	}
	if sub.Drops() != 3 {
		t.Fatalf("Drops = %d, want exactly 3", sub.Drops())
	}
}

// TwoSubscribers_EachReceiveEvent
func TestTether_TwoSubscribersEachReceive(t *testing.T) {
	h := newTestHub(t)
	a := mustSubscribe(t, h, bus, streamhub.SubscribeOptions{})
	b := mustSubscribe(t, h, bus, streamhub.SubscribeOptions{})
	defer a.Close()
	defer b.Close()
	tetherPublish(t, h, "session.started", "s1", "")
	for name, sub := range map[string]streamhub.Subscription{"A": a, "B": b} {
		if it := mustNext(t, sub); it.Record.Name != "session.started" {
			t.Fatalf("sub %s got %+v", name, it)
		}
	}
}

// StoreBacked_RoundTrip (bus_integration_test.go): the hub over a Log that is
// not MemoryLog - here the table-shaped simpleLog with its own store - round
// trips a record, survives a "restart" (a new Log and Hub over the same
// store), and replays from a cursor.
func TestTether_StoreBackedRoundTrip(t *testing.T) {
	ctx := context.Background()
	l := newSimpleLog()
	h := streamhub.New(l)
	sub := mustSubscribe(t, h, bus, streamhub.SubscribeOptions{})
	tetherPublish(t, h, "session.started", "s1", `{"to":"running"}`)
	it := mustNext(t, sub)
	if it.Record.Seq != 1 || it.Record.Name != "session.started" {
		t.Fatalf("live = %+v", it.Record)
	}
	tetherPublish(t, h, "daemon.started", "", "")
	mustNext(t, sub)
	_ = sub.Close()
	if err := h.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	// "Restart": new Log value and Hub over the same stored rows.
	h2 := streamhub.New(&simpleLog{store: l.store})
	defer h2.Shutdown(ctx)
	sub2 := mustSubscribe(t, h2, bus, streamhub.SubscribeOptions{After: 1})
	defer sub2.Close()
	if got := mustNext(t, sub2); got.Record.Seq != 2 || got.Record.Name != "daemon.started" {
		t.Fatalf("replay = %+v", got.Record)
	}
	rec, err := h2.Publish(ctx, bus, streamhub.Event{Name: "next"})
	if err != nil || rec.Seq != 3 {
		t.Fatalf("Publish after restart = %+v, %v; want seq 3", rec, err)
	}
}
