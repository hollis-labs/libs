package hubtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	streamhub "github.com/hollis-labs/go-streamhub"
)

const stressIterations = 1000

func newHub(t *testing.T, f Factory, o ...streamhub.Option) *streamhub.Hub {
	t.Helper()
	return newHubOn(t, f.newLog(t), o...)
}

func newHubOn(t *testing.T, l streamhub.Log, o ...streamhub.Option) *streamhub.Hub {
	t.Helper()
	h := streamhub.New(l, o...)
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })
	return h
}

func publishN(t testing.TB, h *streamhub.Hub, stream string, n int) {
	t.Helper()
	for range n {
		if _, err := h.Publish(context.Background(), stream, streamhub.Event{Name: "e"}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
}

func subscribe(t testing.TB, h *streamhub.Hub, o streamhub.SubscribeOptions) streamhub.Subscription {
	t.Helper()
	sub, err := h.Subscribe(context.Background(), "s", o)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	return sub
}

func wantErr(t testing.TB, sub streamhub.Subscription, target error) {
	t.Helper()
	_ = nextErr(t, sub, target)
}

// nextErr reads one item, requires it to be an error matching target, and
// returns that error.
func nextErr(t testing.TB, sub streamhub.Subscription, target error) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), failTimeout)
	defer cancel()
	it, err := sub.Next(ctx)
	if err == nil {
		t.Fatalf("Next = %+v, want error %v", it, target)
	}
	if !errors.Is(err, target) {
		t.Fatalf("Next error = %v, want %v", err, target)
	}
	return err
}

func wantGap(t testing.TB, it streamhub.Item, reason streamhub.GapReason, missed uint64) streamhub.Gap {
	t.Helper()
	if it.Gap == nil {
		t.Fatalf("got record %d, want a %s gap", it.Record.Seq, reason)
	}
	if it.Gap.Reason != reason || it.Gap.Missed != missed {
		t.Fatalf("gap = %+v, want reason %s missed %d", *it.Gap, reason, missed)
	}
	return *it.Gap
}

func wantRecords(t testing.TB, items []streamhub.Item, first, last streamhub.Seq) {
	t.Helper()
	seqs := Seqs(t, items)
	if uint64(len(seqs)) != uint64(last-first)+1 {
		t.Fatalf("got seqs %v, want %d..%d", seqs, first, last)
	}
	for i, s := range seqs {
		if s != first+streamhub.Seq(i) {
			t.Fatalf("got seqs %v, want %d..%d", seqs, first, last)
		}
	}
}

// HubSuite runs the hub-level behavior on top of the backend f builds:
// the replay/live boundary, gaps, slow policies, terminal handling and
// lifecycle. Call it next to [Conformance].
func HubSuite(t *testing.T, f Factory) {
	t.Helper()
	t.Run("ContiguousUnderConcurrentPublish", func(t *testing.T) { contiguous(t, f) })
	t.Run("ReplayDoesNotHoldStreamLock", func(t *testing.T) { replayNoLock(t, f) })
	t.Run("SlowAppendDoesNotBlockSubscribe", func(t *testing.T) { slowAppend(t, f) })
	t.Run("FromLatestAndFilter", func(t *testing.T) { fromLatestFilter(t, f) })
	t.Run("GapsOnSubscribe", func(t *testing.T) { gapsOnSubscribe(t, f) })
	t.Run("Policies", func(t *testing.T) { policies(t, f) })
	t.Run("Terminal", func(t *testing.T) { terminal(t, f) })
	t.Run("Lifecycle", func(t *testing.T) { lifecycle(t, f) })
}

// contiguous: publishers racing Subscribe(after=x) must yield x+1..N with no
// hole and no duplicate at the replay/live boundary.
func contiguous(t *testing.T, f Factory) {
	h := newHub(t, f)
	ctx := context.Background()
	for i := range stressIterations {
		stream := fmt.Sprintf("s%d", i)
		base := i % 9
		publishN(t, h, stream, base)
		x := streamhub.Seq(i % (base + 1))
		const more = 12
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range more {
				if _, err := h.Publish(ctx, stream, streamhub.Event{}); err != nil {
					t.Errorf("Publish: %v", err)
					return
				}
			}
		}()
		sub, err := h.Subscribe(ctx, stream, streamhub.SubscribeOptions{After: x})
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		total := streamhub.Seq(base + more)
		for want := x + 1; want <= total; want++ {
			it := Next(t, sub)
			if it.Gap != nil || it.Record.Seq != want {
				t.Fatalf("iter %d (after=%d): got %+v, want seq %d", i, x, it, want)
			}
		}
		wg.Wait()
		_ = sub.Close()
	}
}

// replayNoLock: a Log.After parked mid-replay must not block Publish,
// Subscribe or Close of other subscribers on the same stream.
func replayNoLock(t *testing.T, f Factory) {
	g := NewGatedLog(f.newLog(t))
	h := newHubOn(t, g)
	publishN(t, h, "s", 5)
	g.HoldAfter()
	slow := subscribe(t, h, streamhub.SubscribeOptions{})
	select {
	case <-g.AfterEntered():
	case <-time.After(failTimeout):
		t.Fatal("replay never reached Log.After")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		publishN(t, h, "s", 3)
		live := subscribe(t, h, streamhub.SubscribeOptions{After: streamhub.FromLatest})
		publishN(t, h, "s", 1)
		if it := Next(t, live); it.Record.Seq != 9 {
			t.Errorf("live sub got %+v", it)
		}
		if err := live.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(failTimeout):
		t.Fatal("Publish/Subscribe blocked behind a replay in Log.After")
	}

	g.ReleaseAfter()
	wantRecords(t, Drain(t, slow, 9), 1, 9)
}

func slowAppend(t *testing.T, f Factory) {
	g := NewGatedLog(f.newLog(t))
	h := newHubOn(t, g)
	publishN(t, h, "s", 2)
	g.HoldAppend()
	pubDone := make(chan error, 1)
	go func() {
		_, err := h.Publish(context.Background(), "s", streamhub.Event{})
		pubDone <- err
	}()
	// Whether or not the publisher has reached Append yet, Subscribe must
	// return and replay must work.
	sub := subscribe(t, h, streamhub.SubscribeOptions{})
	wantRecords(t, Drain(t, sub, 2), 1, 2)
	g.ReleaseAppend()
	if err := <-pubDone; err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if it := Next(t, sub); it.Record.Seq != 3 {
		t.Fatalf("got %+v, want seq 3", it)
	}
}

func fromLatestFilter(t *testing.T, f Factory) {
	h := newHub(t, f)
	ctx := context.Background()
	publishN(t, h, "s", 4)
	live := subscribe(t, h, streamhub.SubscribeOptions{After: streamhub.FromLatest})
	odd := subscribe(t, h, streamhub.SubscribeOptions{Filter: func(r streamhub.Record) bool { return r.Seq%2 == 1 }})
	for _, n := range []string{"a", "b", "c"} {
		if _, err := h.Publish(ctx, "s", streamhub.Event{Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	items := Drain(t, live, 3)
	wantRecords(t, items, 5, 7)
	got := Seqs(t, Drain(t, odd, 4)) // 1,3 replayed; 5,7 live
	for i, want := range []streamhub.Seq{1, 3, 5, 7} {
		if got[i] != want {
			t.Fatalf("filtered seqs = %v", got)
		}
	}
}

func gapsOnSubscribe(t *testing.T, f Factory) {
	h := newHub(t, f, streamhub.WithRetention(streamhub.Retention{MaxRecords: 3}))
	publishN(t, h, "s", 10)

	sub := subscribe(t, h, streamhub.SubscribeOptions{})
	g := wantGap(t, Next(t, sub), streamhub.GapRetention, 7)
	if g.Requested != 0 || g.OldestAvailable != 8 || g.Latest != 10 {
		t.Fatalf("retention gap = %+v", g)
	}
	wantRecords(t, Drain(t, sub, 3), 8, 10)

	ahead := subscribe(t, h, streamhub.SubscribeOptions{After: 50})
	g = wantGap(t, Next(t, ahead), streamhub.GapCursorAhead, 40)
	if g.Requested != 50 || g.Latest != 10 {
		t.Fatalf("cursor-ahead gap = %+v", g)
	}
	wantRecords(t, Drain(t, ahead, 3), 8, 10)

	strict := subscribe(t, h, streamhub.SubscribeOptions{GapAsError: true})
	err := nextErr(t, strict, streamhub.ErrGap)
	var ge *streamhub.GapError
	if !errors.As(err, &ge) || ge.Gap.Reason != streamhub.GapRetention {
		t.Fatalf("GapAsError err = %v", err)
	}
	wantErr(t, strict, streamhub.ErrClosed)

	// A resume at the boundary is clean.
	ok := subscribe(t, h, streamhub.SubscribeOptions{After: 7})
	wantRecords(t, Drain(t, ok, 3), 8, 10)
}

func policies(t *testing.T, f Factory) {
	ctx := context.Background()

	t.Run("CloseAndResume", func(t *testing.T) {
		h := newHub(t, f)
		sub := subscribe(t, h, streamhub.SubscribeOptions{Buffer: 3}) // default policy
		publishN(t, h, "s", 10)                                       // never blocks
		wantRecords(t, Drain(t, sub, 3), 1, 3)
		err := nextErr(t, sub, streamhub.ErrSlowConsumer)
		var sc *streamhub.SlowConsumerError
		if !errors.As(err, &sc) || sc.LastDelivered != 3 {
			t.Fatalf("err = %v, want LastDelivered 3", err)
		}
		resumed := subscribe(t, h, streamhub.SubscribeOptions{After: sc.LastDelivered, Buffer: 16})
		wantRecords(t, Drain(t, resumed, 7), 4, 10)
	})

	t.Run("DropOldest", func(t *testing.T) {
		h := newHub(t, f)
		sub := subscribe(t, h, streamhub.SubscribeOptions{Buffer: 4, Policy: streamhub.DropOldest})
		publishN(t, h, "s", 10)
		g := wantGap(t, Next(t, sub), streamhub.GapDropped, 6)
		if g.Requested != 0 || g.OldestAvailable != 7 || g.Latest != 10 {
			t.Fatalf("gap = %+v", g)
		}
		wantRecords(t, Drain(t, sub, 4), 7, 10)
		if sub.Drops() != 6 {
			t.Fatalf("Drops = %d, want 6", sub.Drops())
		}
	})

	t.Run("DropNewest", func(t *testing.T) {
		h := newHub(t, f)
		sub := subscribe(t, h, streamhub.SubscribeOptions{Buffer: 4, Policy: streamhub.DropNewest})
		publishN(t, h, "s", 10)
		wantRecords(t, Drain(t, sub, 4), 1, 4)
		g := wantGap(t, Next(t, sub), streamhub.GapDropped, 6)
		if g.Requested != 4 || g.OldestAvailable != 11 || g.Latest != 10 {
			t.Fatalf("gap = %+v", g)
		}
		publishN(t, h, "s", 1)
		if it := Next(t, sub); it.Record.Seq != 11 {
			t.Fatalf("got %+v, want seq 11", it)
		}
		if sub.Drops() != 6 {
			t.Fatalf("Drops = %d, want 6", sub.Drops())
		}
	})

	t.Run("EvictAfterNClosesOnExactlyN", func(t *testing.T) {
		h := newHub(t, f)
		sub := subscribe(t, h, streamhub.SubscribeOptions{Buffer: 2, Policy: streamhub.EvictAfterN(3)})
		publishN(t, h, "s", 4) // 2 fit, 2 drops
		if sub.Drops() != 2 {
			t.Fatalf("Drops = %d, want 2", sub.Drops())
		}
		publishN(t, h, "s", 1) // third consecutive drop: evicted
		g := wantGap(t, Next(t, sub), streamhub.GapDropped, 3)
		if g.OldestAvailable != 4 {
			t.Fatalf("gap = %+v", g)
		}
		wantRecords(t, Drain(t, sub, 2), 4, 5)
		err := nextErr(t, sub, streamhub.ErrSlowConsumer)
		var sc *streamhub.SlowConsumerError
		if !errors.As(err, &sc) || sc.LastDelivered != 5 {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("EvictAfterNCountsConsecutiveOnly", func(t *testing.T) {
		h := newHub(t, f)
		sub := subscribe(t, h, streamhub.SubscribeOptions{Buffer: 2, Policy: streamhub.EvictAfterN(2)})
		publishN(t, h, "s", 3) // 1,2 fit; 3 is a drop (run of 1)
		wantGap(t, Next(t, sub), streamhub.GapDropped, 1)
		wantRecords(t, Drain(t, sub, 2), 2, 3)
		publishN(t, h, "s", 2) // 4,5 accepted: the run is broken
		publishN(t, h, "s", 1) // 6 is a drop (run of 1) - not evicted
		wantGap(t, Next(t, sub), streamhub.GapDropped, 1)
		wantRecords(t, Drain(t, sub, 2), 5, 6)
		publishN(t, h, "s", 4) // 7,8 accepted; 9 drop (1); 10 drop (2): evicted
		if sub.Drops() != 4 {
			t.Fatalf("Drops = %d, want 4", sub.Drops())
		}
		wantGap(t, Next(t, sub), streamhub.GapDropped, 2)
		wantRecords(t, Drain(t, sub, 2), 9, 10)
		wantErr(t, sub, streamhub.ErrSlowConsumer)
	})

	t.Run("BlockWaitsForConsumer", func(t *testing.T) {
		h := newHub(t, f)
		sub := subscribe(t, h, streamhub.SubscribeOptions{Buffer: 1, Policy: streamhub.Block})
		done := make(chan struct{})
		go func() {
			defer close(done)
			publishN(t, h, "s", 50)
		}()
		wantRecords(t, Drain(t, sub, 50), 1, 50)
		<-done
		if sub.Drops() != 0 {
			t.Fatalf("Block lost %d records", sub.Drops())
		}
	})

	t.Run("BlockHonoursContext", func(t *testing.T) {
		h := newHub(t, f)
		sub := subscribe(t, h, streamhub.SubscribeOptions{Buffer: 1, Policy: streamhub.Block})
		publishN(t, h, "s", 1)
		pctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		rec, err := h.Publish(pctx, "s", streamhub.Event{})
		if !errors.Is(err, context.DeadlineExceeded) || rec.Seq != 2 {
			t.Fatalf("Publish = %+v, %v; want seq 2 and DeadlineExceeded", rec, err)
		}
		if it := Next(t, sub); it.Record.Seq != 1 {
			t.Fatalf("got %+v", it)
		}
		wantGap(t, Next(t, sub), streamhub.GapDropped, 1)
	})
}

func terminal(t *testing.T, f Factory) {
	isEnd := func(r streamhub.Record) bool { return r.Name == "end" }
	ctx := context.Background()

	t.Run("TerminalIsLast", func(t *testing.T) {
		h := newHub(t, f, streamhub.WithTerminal(isEnd))
		live := subscribe(t, h, streamhub.SubscribeOptions{})
		publishN(t, h, "s", 2)
		if _, err := h.Publish(ctx, "s", streamhub.Event{Name: "end"}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.Publish(ctx, "s", streamhub.Event{}); !errors.Is(err, streamhub.ErrTerminated) {
			t.Fatalf("Publish after terminal = %v, want ErrTerminated", err)
		}
		wantRecords(t, Drain(t, live, 3), 1, 3)
		wantErr(t, live, io.EOF)

		late := subscribe(t, h, streamhub.SubscribeOptions{After: 1})
		items := Drain(t, late, 2)
		if items[1].Record.Name != "end" {
			t.Fatalf("late replay = %+v", items)
		}
		wantErr(t, late, io.EOF)

		head, err := h.Head(ctx, "s")
		if err != nil || !head.Terminated || head.Latest != 3 {
			t.Fatalf("Head = %+v, %v", head, err)
		}
	})

	t.Run("FinalizerSynthesizes", func(t *testing.T) {
		h := newHub(t, f, streamhub.WithTerminal(isEnd))
		sub := subscribe(t, h, streamhub.SubscribeOptions{})
		publishN(t, h, "s", 2)
		err := h.Close(ctx, "s", streamhub.WithFinalizer(func() (streamhub.Event, bool) {
			return streamhub.Event{Name: "end", Data: []byte("truncated")}, true
		}))
		if err != nil {
			t.Fatal(err)
		}
		items := Drain(t, sub, 3)
		if last := items[2].Record; last.Name != "end" || string(last.Data) != "truncated" {
			t.Fatalf("last = %+v", last)
		}
		wantErr(t, sub, io.EOF)
		if _, err := h.Publish(ctx, "s", streamhub.Event{}); !errors.Is(err, streamhub.ErrTerminated) {
			t.Fatalf("Publish = %v", err)
		}
	})

	t.Run("FinalizerSkippedWhenTerminalPublished", func(t *testing.T) {
		h := newHub(t, f, streamhub.WithTerminal(isEnd))
		publishN(t, h, "s", 1)
		if _, err := h.Publish(ctx, "s", streamhub.Event{Name: "end"}); err != nil {
			t.Fatal(err)
		}
		called := false
		if err := h.Close(ctx, "s", streamhub.WithFinalizer(func() (streamhub.Event, bool) {
			called = true
			return streamhub.Event{Name: "end"}, true
		})); err != nil {
			t.Fatal(err)
		}
		if called {
			t.Fatal("finalizer ran although a terminal record was already published")
		}
		head, _ := h.Head(ctx, "s")
		if head.Latest != 2 {
			t.Fatalf("Latest = %d, want 2", head.Latest)
		}
	})

	t.Run("CloseWithoutTerminalPredicate", func(t *testing.T) {
		h := newHub(t, f)
		sub := subscribe(t, h, streamhub.SubscribeOptions{})
		publishN(t, h, "s", 2)
		if err := h.Close(ctx, "s"); err != nil {
			t.Fatal(err)
		}
		wantRecords(t, Drain(t, sub, 2), 1, 2)
		wantErr(t, sub, io.EOF)
		if err := h.Close(ctx, "s"); err != nil {
			t.Fatalf("second Close = %v", err)
		}
	})
}

// lifecycle: Publish, Subscribe, Close and cancellation racing each other
// must neither panic nor deadlock.
func lifecycle(t *testing.T, f Factory) {
	h := newHub(t, f, streamhub.WithRetainAfterClose(-1))
	ctx := context.Background()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	policiesToTry := []streamhub.SlowPolicy{
		streamhub.CloseAndResume, streamhub.DropOldest, streamhub.DropNewest, streamhub.EvictAfterN(2),
	}
	for w := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := h.Publish(ctx, "s", streamhub.Event{}); err != nil {
					if !errors.Is(err, streamhub.ErrTerminated) {
						t.Errorf("Publish: %v", err)
					}
					return
				}
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 60 {
				cctx, cancel := context.WithCancel(ctx)
				sub, err := h.Subscribe(cctx, "s", streamhub.SubscribeOptions{
					After: streamhub.Seq(i % 3), Buffer: 2, Policy: policiesToTry[(w+i)%len(policiesToTry)],
				})
				if err != nil {
					cancel()
					t.Errorf("Subscribe: %v", err)
					return
				}
				for range i % 5 {
					nctx, ncancel := context.WithTimeout(ctx, time.Millisecond)
					_, _ = sub.Next(nctx)
					ncancel()
				}
				if i%2 == 0 {
					cancel()
				} else {
					_ = sub.Close()
				}
				_, _ = sub.Next(ctx)
				cancel()
			}
		}()
	}
	time.Sleep(20 * time.Millisecond) // let the churn run; the assertion is "no panic, no deadlock"
	if err := h.Close(ctx, "s"); err != nil {
		t.Errorf("Close: %v", err)
	}
	close(stop)
	wg.Wait()
}
