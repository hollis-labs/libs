package streamhub_test

import (
	"context"
	"errors"
	"io"
	"iter"
	"testing"
	"testing/synctest"
	"time"

	streamhub "github.com/hollis-labs/libs/ui-go/streamhub"
	"github.com/hollis-labs/libs/ui-go/streamhub/hubtest"
)

// A slow-consumer close that lands while replay is still running must leave a
// resume cursor with no hole below it: the live records queued behind the
// unfinished replay are discarded, and the log serves them on resume.
func TestSlowCloseDuringReplayResumesWithoutLoss(t *testing.T) {
	ctx := context.Background()
	g := hubtest.NewGatedLog(streamhub.NewMemoryLog())
	h := streamhub.New(g)
	defer h.Shutdown(ctx)
	publishN(t, h, "s", 10)

	g.HoldAfter()
	sub := mustSubscribe(t, h, "s", streamhub.SubscribeOptions{Buffer: 2})
	select {
	case <-g.AfterEntered():
	case <-time.After(30 * time.Second):
		t.Fatal("replay did not start")
	}
	publishN(t, h, "s", 5) // overflows the 2-record live queue behind the parked replay
	g.ReleaseAfter()

	var last streamhub.Seq
	for {
		it, err := sub.Next(ctx)
		if err != nil {
			var sc *streamhub.SlowConsumerError
			if !errors.As(err, &sc) {
				t.Fatalf("Next = %v, want SlowConsumerError", err)
			}
			if sc.LastDelivered != last {
				t.Fatalf("LastDelivered = %d, but the last record handed out was %d", sc.LastDelivered, last)
			}
			break
		}
		if it.Record.Seq != last+1 {
			t.Fatalf("hole: got %d after %d", it.Record.Seq, last)
		}
		last = it.Record.Seq
	}
	resumed := mustSubscribe(t, h, "s", streamhub.SubscribeOptions{After: last, Buffer: 64})
	defer resumed.Close()
	for want := last + 1; want <= 15; want++ {
		if got := mustNext(t, resumed).Record.Seq; got != want {
			t.Fatalf("resume delivered %d, want %d", got, want)
		}
	}
}

// The grace timer must not pull a stream out from under a replay that is
// still reading it: forgetting waits for the last replay.
func TestForgetWaitsForRunningReplay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		mem := streamhub.NewMemoryLog()
		g := hubtest.NewGatedLog(mem)
		h := streamhub.New(g, streamhub.WithRetainAfterClose(time.Second))
		publishN(t, h, "s", 3)
		if err := h.Close(ctx, "s"); err != nil {
			t.Fatal(err)
		}
		g.HoldAfter()
		late := mustSubscribe(t, h, "s", streamhub.SubscribeOptions{})
		<-g.AfterEntered()

		time.Sleep(5 * time.Second) // the grace timer fires while the replay is parked
		synctest.Wait()
		if head, _ := mem.Head(ctx, "s"); head.Latest != 3 {
			t.Fatalf("stream forgotten under a running replay: %+v", head)
		}
		g.ReleaseAfter()
		for want := streamhub.Seq(1); want <= 3; want++ {
			if got := mustNext(t, late).Record.Seq; got != want {
				t.Fatalf("replay got %d, want %d", got, want)
			}
		}
		if _, err := late.Next(ctx); !errors.Is(err, io.EOF) {
			t.Fatalf("Next = %v, want EOF", err)
		}
		synctest.Wait()
		if head, _ := mem.Head(ctx, "s"); head.Latest != 0 {
			t.Fatalf("stream not forgotten after the replay finished: %+v", head)
		}
		_ = h.Shutdown(ctx)
	})
}

var errBoom = errors.New("boom")

// failingLog yields two records and then a hard (non-gap) error from After.
type failingLog struct{ streamhub.Log }

func (f failingLog) After(ctx context.Context, stream string, after streamhub.Seq) iter.Seq2[streamhub.Record, error] {
	return func(yield func(streamhub.Record, error) bool) {
		n := 0
		for r, err := range f.Log.After(ctx, stream, after) {
			if n == 2 {
				yield(streamhub.Record{}, errBoom)
				return
			}
			if !yield(r, err) {
				return
			}
			n++
		}
	}
}

func TestReplayFailureSurfacesAfterDeliveredRecords(t *testing.T) {
	ctx := context.Background()
	h := streamhub.New(failingLog{streamhub.NewMemoryLog()})
	defer h.Shutdown(ctx)
	publishN(t, h, "s", 5)
	sub := mustSubscribe(t, h, "s", streamhub.SubscribeOptions{})
	defer sub.Close()
	for want := streamhub.Seq(1); want <= 2; want++ {
		if got := mustNext(t, sub).Record.Seq; got != want {
			t.Fatalf("got %d, want %d", got, want)
		}
	}
	if _, err := sub.Next(ctx); !errors.Is(err, errBoom) {
		t.Fatalf("Next = %v, want errBoom", err)
	}
	if _, err := sub.Next(ctx); !errors.Is(err, errBoom) {
		t.Fatalf("second Next = %v, want the same error", err)
	}
}

// Filtering applies to replay as well as to live records.
func TestFilterAppliesToReplayAndLive(t *testing.T) {
	ctx := context.Background()
	h := newTestHub(t)
	for _, n := range []string{"keep", "skip", "keep", "skip"} {
		if _, err := h.Publish(ctx, "s", streamhub.Event{Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	sub := mustSubscribe(t, h, "s", streamhub.SubscribeOptions{Filter: func(r streamhub.Record) bool { return r.Name == "keep" }})
	defer sub.Close()
	if _, err := h.Publish(ctx, "s", streamhub.Event{Name: "keep"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []streamhub.Seq{1, 3, 5} {
		if got := mustNext(t, sub).Record.Seq; got != want {
			t.Fatalf("got %d, want %d", got, want)
		}
	}
}
