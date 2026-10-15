package tooluse

// CW-20260929-0012 #4: a multi-tool-call turn runs its concurrent-safe calls
// on a bounded number of goroutines, not one per call.

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// peakGauge tracks how many callers are inside enter/leave at once.
type peakGauge struct{ cur, peak atomic.Int64 }

func (g *peakGauge) enter() {
	n := g.cur.Add(1)
	for {
		p := g.peak.Load()
		if n <= p || g.peak.CompareAndSwap(p, n) {
			return
		}
	}
}
func (g *peakGauge) leave() { g.cur.Add(-1) }

func TestRunBounded_PeakNeverExceedsLimit(t *testing.T) {
	for _, tc := range []struct{ n, limit int }{{40, 3}, {40, 1}, {5, 8}, {8, 8}, {1, 4}} {
		t.Run(fmt.Sprintf("n%d_limit%d", tc.n, tc.limit), func(t *testing.T) {
			var g peakGauge
			var ran atomic.Int64
			RunBounded(context.Background(), tc.n, tc.limit,
				func(int) {
					g.enter()
					defer g.leave()
					ran.Add(1)
					time.Sleep(2 * time.Millisecond)
				},
				func(int) { t.Error("nothing should be skipped without cancellation") })
			if int(ran.Load()) != tc.n {
				t.Errorf("ran %d items, want %d", ran.Load(), tc.n)
			}
			want := int64(min(tc.limit, tc.n))
			if p := g.peak.Load(); p > want {
				t.Errorf("peak concurrency %d exceeds limit %d", p, want)
			}
		})
	}
}

// Slots hold results by index, so which worker ran an item cannot reorder them.
func TestRunBounded_ResultsKeepIndexOrder(t *testing.T) {
	const n = 50
	got := make([]int, n)
	RunBounded(context.Background(), n, 4, func(i int) {
		time.Sleep(time.Duration(n-i) * 50 * time.Microsecond) // later items finish first
		got[i] = i * 2
	}, func(int) {})
	for i, v := range got {
		if v != i*2 {
			t.Fatalf("slot %d = %d, want %d", i, v, i*2)
		}
	}
}

// A panic in one item is confined to it: the rest still run, and the call returns.
func TestRunBounded_PanicConfinedToItem(t *testing.T) {
	var ran atomic.Int64
	RunBounded(context.Background(), 10, 2, func(i int) {
		if i == 3 {
			panic("boom")
		}
		ran.Add(1)
	}, func(int) {})
	if ran.Load() != 9 {
		t.Errorf("ran %d of the 9 non-panicking items", ran.Load())
	}
}

// Cancel with work queued: running items finish, queued items are skipped
// (each exactly once), every index is accounted for, and the call returns
// having waited for the running ones.
func TestRunBounded_CancellationDrainsCleanly(t *testing.T) {
	const n, limit = 30, 3
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var g peakGauge
	var startedCount atomic.Int64
	started := make(chan struct{}, n)
	release := make(chan struct{})
	seen := make([]atomic.Int64, n) // times each index was run or skipped
	var running atomic.Int64

	done := make(chan struct{})
	go func() {
		defer close(done)
		RunBounded(ctx, n, limit,
			func(i int) {
				g.enter()
				defer g.leave()
				seen[i].Add(1)
				startedCount.Add(1)
				running.Add(1)
				started <- struct{}{}
				<-release
				running.Add(-1)
			},
			func(i int) { seen[i].Add(1) })
	}()

	for i := 0; i < limit; i++ { // the first `limit` items are now running, the rest queued
		<-started
	}
	cancel()
	select {
	case <-done:
		t.Fatal("runBounded returned while items were still running")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runBounded did not drain after cancellation")
	}

	if running.Load() != 0 {
		t.Errorf("%d items still running after return", running.Load())
	}
	if startedCount.Load() != limit {
		t.Errorf("%d items started, want %d (the rest were queued at cancel)", startedCount.Load(), limit)
	}
	for i := range seen {
		if c := seen[i].Load(); c != 1 {
			t.Errorf("index %d handled %d times, want exactly 1", i, c)
		}
	}
	if p := g.peak.Load(); p > limit {
		t.Errorf("peak concurrency %d exceeds limit %d", p, limit)
	}
}
