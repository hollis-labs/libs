package streamhub_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	streamhub "github.com/hollis-labs/go-streamhub"
	"github.com/hollis-labs/go-streamhub/hubtest"
)

// The concurrency suite: run it with `go test -race -count=20 -run Concurrency`.
// Tests inside a synctest bubble double as leak detectors: the bubble cannot
// end while any goroutine started inside it (replay goroutines, timers'
// callbacks, workers) is still blocked, so a leak is a test failure.

func TestConcurrency_HubSuiteOnMemoryLog(t *testing.T) {
	hubtest.HubSuite(t, memoryFactory(8))
}

// Publish, Subscribe, Next, Close, context cancel, stream Close and Shutdown
// all racing: no panic (in particular no send on a closed channel), no
// deadlock, no leaked goroutine, and every record a subscriber does receive
// is in strictly increasing Seq order.
func TestConcurrency_ChurnNoPanicNoLeak(t *testing.T) {
	for seed := uint64(1); seed <= 4; seed++ {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) { churn(t, seed) })
		})
	}
}

func churn(t *testing.T, seed uint64) {
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(seed, seed*7919))
	// Never zero: a zero sleep would spin without letting virtual time advance.
	jitter := func() time.Duration { return time.Duration(1+rng.IntN(4)) * time.Millisecond }
	policies := []streamhub.SlowPolicy{
		streamhub.CloseAndResume, streamhub.DropOldest, streamhub.DropNewest,
		streamhub.EvictAfterN(3), streamhub.Block,
	}
	h := streamhub.New(streamhub.NewMemoryLog(),
		streamhub.WithRetention(streamhub.Retention{MaxRecords: 32}),
		streamhub.WithTerminal(func(r streamhub.Record) bool { return r.Name == "end" }),
		streamhub.WithRetainAfterClose(50*time.Millisecond))

	var wg sync.WaitGroup
	stop := make(chan struct{})
	streams := []string{"a", "b"}

	for p := range 3 {
		delay := jitter()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				pctx, cancel := context.WithTimeout(ctx, 3*time.Millisecond) // bounds Block waits
				_, err := h.Publish(pctx, streams[(p+i)%2], streamhub.Event{Name: "e"})
				cancel()
				if err != nil && !errors.Is(err, context.DeadlineExceeded) &&
					!errors.Is(err, streamhub.ErrTerminated) && !errors.Is(err, streamhub.ErrClosed) {
					t.Errorf("Publish: %v", err)
					return
				}
				time.Sleep(delay)
			}
		}()
	}

	for s := range 8 {
		wr := rand.New(rand.NewPCG(seed, uint64(s)+1000)) // one source per goroutine
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 40 {
				cctx, cancel := context.WithCancel(ctx)
				sub, err := h.Subscribe(cctx, streams[(s+i)%2], streamhub.SubscribeOptions{
					After:  streamhub.Seq(wr.IntN(3)),
					Buffer: 1 + wr.IntN(4), Policy: policies[(s+i)%len(policies)],
					Filter: func(r streamhub.Record) bool { return r.Seq%3 != 1 },
				})
				if err != nil {
					cancel()
					if !errors.Is(err, streamhub.ErrClosed) {
						t.Errorf("Subscribe: %v", err)
					}
					return
				}
				var last streamhub.Seq
				for n := wr.IntN(6); n > 0; n-- {
					nctx, ncancel := context.WithTimeout(ctx, 2*time.Millisecond)
					it, err := sub.Next(nctx)
					ncancel()
					if err != nil {
						break
					}
					if it.Gap == nil {
						if it.Record.Seq <= last {
							t.Errorf("out of order: %d after %d", it.Record.Seq, last)
							break
						}
						last = it.Record.Seq
					}
				}
				switch i % 3 {
				case 0:
					cancel()
				case 1:
					_ = sub.Close()
				}
				_, _ = sub.Next(ctx) // never blocks: the subscription is closed or ...
				_ = sub.Close()
				cancel()
			}
		}()
	}

	wg.Add(1)
	go func() { // ends stream "a" mid-flight, then reopens it via AutoOpen
		defer wg.Done()
		time.Sleep(20 * time.Millisecond)
		if err := h.Close(ctx, "a"); err != nil && !errors.Is(err, streamhub.ErrUnknownStream) {
			t.Errorf("Close: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
		_, _ = h.Publish(ctx, "a", streamhub.Event{Name: "end"})
	}()

	time.Sleep(300 * time.Millisecond)
	close(stop)
	// Shutdown while workers may still be inside Subscribe/Next.
	if err := h.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	wg.Wait()
}

// Cancelling the Subscribe context while replay is parked inside Log.After
// ends the replay goroutine.
func TestConcurrency_CancelDuringReplayLeaksNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		g := hubtest.NewGatedLog(streamhub.NewMemoryLog())
		h := streamhub.New(g)
		publishN(t, h, "s", 3)
		g.HoldAfter()
		cctx, cancel := context.WithCancel(ctx)
		sub, err := h.Subscribe(cctx, "s", streamhub.SubscribeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		<-g.AfterEntered()
		cancel()
		if _, err := sub.Next(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("Next = %v, want context.Canceled", err)
		}
		if err := h.Shutdown(ctx); err != nil { // waits for the replay goroutine
			t.Fatal(err)
		}
	})
}

// Shutdown with a Block publisher parked, a replay parked and a live
// subscriber: everything is released and nothing leaks.
func TestConcurrency_ShutdownReleasesEverything(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		g := hubtest.NewGatedLog(streamhub.NewMemoryLog())
		h := streamhub.New(g)
		publishN(t, h, "s", 2)
		blocker := mustSubscribe(t, h, "s", streamhub.SubscribeOptions{Buffer: 1, Policy: streamhub.Block, After: streamhub.FromLatest})
		publishN(t, h, "s", 1) // fills the blocker
		g.HoldAfter()
		replayer := mustSubscribe(t, h, "s", streamhub.SubscribeOptions{})
		<-g.AfterEntered()

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.Publish(ctx, "s", streamhub.Event{})
			if err != nil && !errors.Is(err, streamhub.ErrClosed) {
				t.Errorf("Publish: %v", err)
			}
		}()
		synctest.Wait()
		if err := h.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
		wg.Wait()
		for _, sub := range []streamhub.Subscription{blocker, replayer} {
			for {
				_, err := sub.Next(ctx)
				if errors.Is(err, streamhub.ErrClosed) {
					break
				}
				if err != nil && !errors.Is(err, io.EOF) {
					t.Fatalf("Next = %v", err)
				}
				if err != nil {
					t.Fatalf("Next = EOF, want ErrClosed")
				}
			}
		}
	})
}
