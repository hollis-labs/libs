package streamhub_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	streamhub "github.com/hollis-labs/go-streamhub"
)

func TestSlowPolicyString(t *testing.T) {
	for p, want := range map[streamhub.SlowPolicy]string{
		{}:                        "CloseAndResume",
		streamhub.CloseAndResume:  "CloseAndResume",
		streamhub.Block:           "Block",
		streamhub.DropNewest:      "DropNewest",
		streamhub.DropOldest:      "DropOldest",
		streamhub.EvictAfterN(64): "EvictAfterN(64)",
		streamhub.EvictAfterN(-5): "EvictAfterN(1)",
	} {
		if got := p.String(); got != want {
			t.Errorf("%v.String() = %q, want %q", p, got, want)
		}
	}
}

// A subscriber that never reads must not stall Publish under any lossy
// policy; the bubble reports a blocked Publish as a deadlock, not a timeout.
func TestLossyPoliciesNeverBlockPublish(t *testing.T) {
	for _, p := range []streamhub.SlowPolicy{
		streamhub.CloseAndResume, streamhub.DropNewest, streamhub.DropOldest, streamhub.EvictAfterN(5),
	} {
		t.Run(p.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := streamhub.New(streamhub.NewMemoryLog())
				sub := mustSubscribe(t, h, "s", streamhub.SubscribeOptions{Buffer: 2, Policy: p})
				done := false
				go func() {
					publishN(t, h, "s", 5000)
					done = true
				}()
				synctest.Wait()
				if !done {
					t.Fatal("Publish blocked on a subscriber that never reads")
				}
				_ = sub.Close()
				if err := h.Shutdown(context.Background()); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

// Block: the wait ends exactly at the publisher's deadline (virtual time),
// the record is still appended, and the subscriber is told about the loss.
func TestBlockWaitEndsAtDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := streamhub.New(streamhub.NewMemoryLog())
		sub := mustSubscribe(t, h, "s", streamhub.SubscribeOptions{Buffer: 1, Policy: streamhub.Block})
		publishN(t, h, "s", 1)

		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rec, err := h.Publish(ctx, "s", streamhub.Event{})
		if !errors.Is(err, context.DeadlineExceeded) || rec.Seq != 2 {
			t.Fatalf("Publish = %+v, %v", rec, err)
		}
		if got := time.Since(start); got != 5*time.Second {
			t.Fatalf("blocked for %v, want exactly 5s of virtual time", got)
		}
		if it := mustNext(t, sub); it.Record.Seq != 1 {
			t.Fatalf("item = %+v", it)
		}
		if it := mustNext(t, sub); it.Gap == nil || it.Gap.Reason != streamhub.GapDropped || it.Gap.Missed != 1 {
			t.Fatalf("item = %+v, want dropped gap of 1", it)
		}
		_ = sub.Close()
		_ = h.Shutdown(context.Background())
	})
}

// Blocked publishers are released by Close of the blocking subscription and
// by Shutdown.
func TestBlockReleasedByCloseAndShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := streamhub.New(streamhub.NewMemoryLog())
		sub := mustSubscribe(t, h, "s", streamhub.SubscribeOptions{Buffer: 1, Policy: streamhub.Block})
		publishN(t, h, "s", 1)
		var pubErr error
		published := false
		go func() {
			_, pubErr = h.Publish(context.Background(), "s", streamhub.Event{})
			published = true
		}()
		synctest.Wait()
		if published {
			t.Fatal("Publish returned while the Block subscriber was full")
		}
		_ = sub.Close()
		synctest.Wait()
		if !published || pubErr != nil {
			t.Fatalf("Publish after Close: done=%v err=%v", published, pubErr)
		}

		sub2 := mustSubscribe(t, h, "s", streamhub.SubscribeOptions{Buffer: 1, Policy: streamhub.Block, After: streamhub.FromLatest})
		publishN(t, h, "s", 1)
		published = false
		go func() {
			_, pubErr = h.Publish(context.Background(), "s", streamhub.Event{})
			published = true
		}()
		synctest.Wait()
		if published {
			t.Fatal("second Publish did not block")
		}
		if err := h.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if !published {
			t.Fatal("Shutdown did not release the blocked Publish")
		}
		_ = sub2.Close()
	})
}

// Conservation: for the lossy policies, delivered records plus the gaps'
// Missed counts account for every published record, in order, with each gap
// placed exactly where the loss happened.
func TestDropPoliciesConserveRecords(t *testing.T) {
	for _, p := range []streamhub.SlowPolicy{streamhub.DropOldest, streamhub.DropNewest} {
		t.Run(p.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := streamhub.New(streamhub.NewMemoryLog())
				sub := mustSubscribe(t, h, "s", streamhub.SubscribeOptions{Buffer: 3, Policy: p})
				const total = 2000
				var got, missed uint64
				var last streamhub.Seq
				read := func(n int) {
					for range n {
						ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
						it, err := sub.Next(ctx)
						cancel()
						if err != nil {
							return
						}
						if it.Gap != nil {
							if it.Gap.Reason != streamhub.GapDropped {
								t.Fatalf("gap %+v", *it.Gap)
							}
							missed += it.Gap.Missed
							if it.Gap.Requested != last {
								t.Fatalf("gap.Requested = %d, want last delivered %d", it.Gap.Requested, last)
							}
							continue
						}
						if it.Record.Seq <= last {
							t.Fatalf("out of order: %d after %d", it.Record.Seq, last)
						}
						last = it.Record.Seq
						got++
					}
				}
				for i := range total {
					publishN(t, h, "s", 1)
					if i%7 == 0 {
						read(i % 5)
					}
				}
				read(100)
				if got+missed != total {
					t.Fatalf("delivered %d + missed %d != %d published", got, missed, total)
				}
				if sub.Drops() != missed {
					t.Fatalf("Drops() = %d, gaps reported %d", sub.Drops(), missed)
				}
				_ = h.Shutdown(context.Background())
			})
		})
	}
}
