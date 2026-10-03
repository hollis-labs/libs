package streamhub_test

// Scenarios ported from Nanite's service/stream_replay_test.go (StreamManager
// over chat.StreamEvent), re-expressed against Hub + MemoryLog. One stream is
// one Nanite message stream; JSON-encoded chat events would ride in Data.

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	streamhub "github.com/hollis-labs/go-streamhub"
)

// AssignsMonotonicEventIDs: the hub stamps 1, 2, 3, ... in publish order.
func TestNanite_AssignsMonotonicSeq(t *testing.T) {
	ctx := context.Background()
	h := newTestHub(t)
	sub := mustSubscribe(t, h, "msg-A", streamhub.SubscribeOptions{})
	defer sub.Close()
	for _, c := range []string{"one", "two", "three"} {
		if _, err := h.Publish(ctx, "msg-A", streamhub.Event{Name: "delta", Data: []byte(c)}); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range []string{"one", "two", "three"} {
		it := mustNext(t, sub)
		if it.Record.Seq != streamhub.Seq(i+1) || string(it.Record.Data) != want {
			t.Fatalf("item %d = %+v", i, it.Record)
		}
	}
}

// ReplaysAfterCursorReconnect: a client that saw 1..3 reconnects with 3 and
// gets 4..6, not 1..6.
func TestNanite_ReplaysAfterCursorReconnect(t *testing.T) {
	h := newTestHub(t)
	sub1 := mustSubscribe(t, h, "msg-B", streamhub.SubscribeOptions{})
	publishN(t, h, "msg-B", 3)
	var last streamhub.Seq
	for range 3 {
		last = mustNext(t, sub1).Record.Seq
	}
	_ = sub1.Close()
	publishN(t, h, "msg-B", 3) // while disconnected

	sub2 := mustSubscribe(t, h, "msg-B", streamhub.SubscribeOptions{After: last})
	defer sub2.Close()
	for want := streamhub.Seq(4); want <= 6; want++ {
		if got := mustNext(t, sub2).Record.Seq; got != want {
			t.Fatalf("replayed %d, want %d", got, want)
		}
	}
}

// LiveAfterReplay: a subscription sees the replayed backlog and then the
// records published after it subscribed, in order.
func TestNanite_LiveAfterReplay(t *testing.T) {
	h := newTestHub(t)
	publishN(t, h, "msg-C", 3)
	sub := mustSubscribe(t, h, "msg-C", streamhub.SubscribeOptions{})
	defer sub.Close()
	publishN(t, h, "msg-C", 2)
	for want := streamhub.Seq(1); want <= 5; want++ {
		it := mustNext(t, sub)
		if it.Gap != nil || it.Record.Seq != want {
			t.Fatalf("got %+v, want seq %d", it, want)
		}
	}
}

// RingEvicts: with a 256-record ring, a client that reconnects from 0 after
// 522 events gets an explicit retention gap and then exactly the last 256 -
// Nanite's ring evicted silently.
func TestNanite_RingEvictsWithGap(t *testing.T) {
	const ring = 256
	const total = ring*2 + 10
	ctx := context.Background()
	h := newTestHub(t, streamhub.WithRetention(streamhub.Retention{MaxRecords: ring}))
	sub1 := mustSubscribe(t, h, "msg-D", streamhub.SubscribeOptions{})
	defer sub1.Close()
	for i := range total {
		if _, err := h.Publish(ctx, "msg-D", streamhub.Event{Name: "delta"}); err != nil {
			t.Fatal(err)
		}
		if it := mustNext(t, sub1); it.Record.Seq != streamhub.Seq(i+1) {
			t.Fatalf("sub1 drain %d: %+v", i, it)
		}
	}
	sub2 := mustSubscribe(t, h, "msg-D", streamhub.SubscribeOptions{})
	defer sub2.Close()
	gap := mustNext(t, sub2).Gap
	if gap == nil || gap.Reason != streamhub.GapRetention || gap.Missed != total-ring || gap.OldestAvailable != total-ring+1 {
		t.Fatalf("gap = %+v", gap)
	}
	for want := streamhub.Seq(total - ring + 1); want <= total; want++ {
		if got := mustNext(t, sub2).Record.Seq; got != want {
			t.Fatalf("replayed %d, want %d", got, want)
		}
	}
}

// SubscribeAfterClose: after the producer finishes, a late subscriber still
// gets the whole replay, then EOF (the sleeping-tab case).
func TestNanite_SubscribeAfterClose(t *testing.T) {
	ctx := context.Background()
	h := newTestHub(t)
	for _, c := range []string{"p", "q"} {
		if _, err := h.Publish(ctx, "msg-E", streamhub.Event{Data: []byte(c)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.Close(ctx, "msg-E"); err != nil {
		t.Fatal(err)
	}
	sub := mustSubscribe(t, h, "msg-E", streamhub.SubscribeOptions{})
	defer sub.Close()
	if a, b := mustNext(t, sub), mustNext(t, sub); string(a.Record.Data) != "p" || string(b.Record.Data) != "q" {
		t.Fatalf("replay = %q then %q", a.Record.Data, b.Record.Data)
	}
	if _, err := sub.Next(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("Next = %v, want EOF", err)
	}
}

// TakeoverClosesPriorSubscriber: single-reader-per-stream is an application
// policy (Nanite's RegisterSSE); the hub supports it as "app closes the old
// subscription". The old one ends, the new one is unaffected.
func TestNanite_TakeoverClosesPriorSubscriber(t *testing.T) {
	ctx := context.Background()
	h := newTestHub(t)
	sub1 := mustSubscribe(t, h, "msg-F", streamhub.SubscribeOptions{})
	sub2 := mustSubscribe(t, h, "msg-F", streamhub.SubscribeOptions{})
	defer sub2.Close()
	if err := sub1.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := sub1.Next(ctx); !errors.Is(err, streamhub.ErrClosed) {
		t.Fatalf("sub1.Next = %v, want ErrClosed", err)
	}
	publishN(t, h, "msg-F", 1)
	if it := mustNext(t, sub2); it.Record.Seq != 1 {
		t.Fatalf("sub2 = %+v", it)
	}
}

// NoRaceOnTakeoverDuringFanout: a producer publishes while reconnectors keep
// subscribing and closing the previous subscription. Meaningful under -race:
// admission and fan-out share one lock, so there is no send-on-closed and no
// data race, and no recover() is needed.
func TestNanite_NoRaceOnTakeoverDuringFanout(t *testing.T) {
	const events, reconnectors, reconnectsEach = 500, 4, 100
	ctx := context.Background()
	h := newTestHub(t)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range events {
			if _, err := h.Publish(ctx, "msg-race", streamhub.Event{Name: "delta"}); err != nil {
				t.Errorf("Publish: %v", err)
				return
			}
		}
	}()
	for range reconnectors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var prev streamhub.Subscription
			for range reconnectsEach {
				sub, err := h.Subscribe(ctx, "msg-race", streamhub.SubscribeOptions{Buffer: 4, Policy: streamhub.DropOldest})
				if err != nil {
					t.Errorf("Subscribe: %v", err)
					return
				}
				if prev != nil {
					_ = prev.Close()
				}
				prev = sub
				nctx, cancel := context.WithTimeout(ctx, 200*time.Microsecond)
				_, _ = sub.Next(nctx)
				cancel()
			}
			if prev != nil {
				_ = prev.Close()
			}
		}()
	}
	wg.Wait()
}

// ActiveMessageSkipsCompletedReplayWindow is NOT expressible: it tests
// StreamManager.ActiveMessageForSession, an index from session to message
// stream that lives above the hub. What the test relies on underneath, that
// a finished stream leaves the replay window after its grace period, is
// ported here.
func TestNanite_CompletedStreamLeavesReplayWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		l := streamhub.NewMemoryLog()
		h := streamhub.New(l, streamhub.WithRetainAfterClose(60*time.Second)) // Nanite's defaultPostCompletionGrace
		publishN(t, h, "completed", 1)
		if err := h.Close(ctx, "completed"); err != nil {
			t.Fatal(err)
		}
		publishN(t, h, "active", 1)
		time.Sleep(61 * time.Second)
		synctest.Wait()
		if head, _ := l.Head(ctx, "completed"); head.Latest != 0 {
			t.Fatalf("completed stream still in the window: %+v", head)
		}
		if head, _ := l.Head(ctx, "active"); head.Latest != 1 {
			t.Fatalf("active stream lost: %+v", head)
		}
		_ = h.Shutdown(ctx)
	})
}
