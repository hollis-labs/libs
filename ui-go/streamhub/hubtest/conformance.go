package hubtest

import (
	"context"
	"errors"
	"sync"
	"testing"

	streamhub "github.com/hollis-labs/go-streamhub"
)

// Factory builds the backend under test.
type Factory struct {
	// New returns a fresh, empty Log. The suite closes it when the test ends.
	New func(t *testing.T) streamhub.Log
	// Reopen returns a Log over the same stored data as l, as after a
	// process restart. It may close l. Leave it nil for a backend with no
	// persistence to skip the reopen case (MemoryLog returns l itself).
	Reopen func(t *testing.T, l streamhub.Log) streamhub.Log
}

func (f Factory) newLog(t *testing.T) streamhub.Log {
	t.Helper()
	l := f.New(t)
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func appendN(t *testing.T, l streamhub.Log, stream string, n int) []streamhub.Record {
	t.Helper()
	out := make([]streamhub.Record, 0, n)
	for i := range n {
		r, err := l.Append(context.Background(), stream, streamhub.Event{Name: "n", Data: []byte{byte(i)}})
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		out = append(out, r)
	}
	return out
}

// after collects After(after) and returns the records read before any error,
// plus that error.
func after(l streamhub.Log, stream string, cursor streamhub.Seq) ([]streamhub.Record, error) {
	var out []streamhub.Record
	for r, err := range l.After(context.Background(), stream, cursor) {
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

func wantSeqs(t *testing.T, recs []streamhub.Record, first, last streamhub.Seq) {
	t.Helper()
	if last < first {
		if len(recs) != 0 {
			t.Fatalf("got %d records, want none", len(recs))
		}
		return
	}
	if uint64(len(recs)) != uint64(last-first)+1 {
		t.Fatalf("got %d records, want %d..%d", len(recs), first, last)
	}
	for i, r := range recs {
		if r.Seq != first+streamhub.Seq(i) {
			t.Fatalf("record %d has Seq %d, want %d", i, r.Seq, first+streamhub.Seq(i))
		}
	}
}

// Conformance runs the Log contract against the backend f builds.
func Conformance(t *testing.T, f Factory) {
	t.Helper()
	ctx := context.Background()

	t.Run("SeqStartsAtOnePerStream", func(t *testing.T) {
		l := f.newLog(t)
		a := appendN(t, l, "a", 3)
		b := appendN(t, l, "b", 2)
		wantSeqs(t, a, 1, 3)
		wantSeqs(t, b, 1, 2)
		appendN(t, l, "a", 1)
		ha, _ := l.Head(ctx, "a")
		hb, _ := l.Head(ctx, "b")
		if ha.Latest != 4 || hb.Latest != 2 {
			t.Fatalf("heads a=%+v b=%+v", ha, hb)
		}
	})

	t.Run("AppendReturnsStoredRecord", func(t *testing.T) {
		l := f.newLog(t)
		data := []byte("payload")
		r, err := l.Append(ctx, "s", streamhub.Event{Name: "ev", Data: data})
		if err != nil {
			t.Fatal(err)
		}
		data[0] = 'X' // the log must not alias the caller's slice
		if r.Name != "ev" || r.Seq != 1 || r.At.IsZero() {
			t.Fatalf("record = %+v", r)
		}
		got, err := after(l, "s", 0)
		if err != nil || len(got) != 1 || got[0].Name != "ev" || string(got[0].Data) != "payload" || got[0].Seq != 1 {
			t.Fatalf("stored = %+v err=%v", got, err)
		}
	})

	t.Run("AfterIsExactlyAfterThroughHead", func(t *testing.T) {
		l := f.newLog(t)
		appendN(t, l, "s", 10)
		for _, x := range []streamhub.Seq{0, 1, 4, 9} {
			got, err := after(l, "s", x)
			if err != nil {
				t.Fatalf("After(%d): %v", x, err)
			}
			wantSeqs(t, got, x+1, 10)
		}
		got, err := after(l, "s", 10)
		if err != nil || len(got) != 0 {
			t.Fatalf("After(head) = %v, %v; want empty", got, err)
		}
	})

	t.Run("CursorAhead", func(t *testing.T) {
		l := f.newLog(t)
		appendN(t, l, "s", 3)
		for _, n := range []streamhub.Seq{1, 5} {
			got, err := after(l, "s", 3+n)
			if !errors.Is(err, streamhub.ErrCursorAhead) || len(got) != 0 {
				t.Fatalf("After(head+%d) = %v, %v; want ErrCursorAhead and no records", n, got, err)
			}
		}
	})

	t.Run("UnknownStreamIsEmpty", func(t *testing.T) {
		l := f.newLog(t)
		h, err := l.Head(ctx, "nope")
		if err != nil || h != (streamhub.Head{}) {
			t.Fatalf("Head = %+v, %v", h, err)
		}
		if got, err := after(l, "nope", 0); err != nil || len(got) != 0 {
			t.Fatalf("After(0) = %v, %v", got, err)
		}
		if _, err := after(l, "nope", 1); !errors.Is(err, streamhub.ErrCursorAhead) {
			t.Fatalf("After(1) err = %v, want ErrCursorAhead", err)
		}
	})

	t.Run("TrimGapNeverPartial", func(t *testing.T) {
		l := f.newLog(t)
		appendN(t, l, "s", 10)
		if err := l.Trim(ctx, "s", streamhub.Retention{MaxRecords: 4}); err != nil {
			t.Fatal(err)
		}
		h, _ := l.Head(ctx, "s")
		if h.Latest != 10 || h.PrunedThrough != 6 || h.Dropped != 6 {
			t.Fatalf("head = %+v, want Latest 10, PrunedThrough 6, Dropped 6", h)
		}
		for _, j := range []streamhub.Seq{0, 3, 5} {
			got, err := after(l, "s", j)
			if !errors.Is(err, streamhub.ErrGap) || len(got) != 0 {
				t.Fatalf("After(%d) = %d records, %v; want ErrGap and no records", j, len(got), err)
			}
			var ge *streamhub.GapError
			if errors.As(err, &ge) && (ge.Gap.Reason != streamhub.GapRetention || ge.Gap.OldestAvailable != 7) {
				t.Fatalf("gap = %+v", ge.Gap)
			}
		}
		got, err := after(l, "s", 6)
		if err != nil {
			t.Fatalf("After(pruned): %v", err)
		}
		wantSeqs(t, got, 7, 10)
		// The cursor keeps advancing across a trim.
		r, _ := l.Append(ctx, "s", streamhub.Event{})
		if r.Seq != 11 {
			t.Fatalf("Seq after trim = %d, want 11", r.Seq)
		}
	})

	t.Run("TrimByBytes", func(t *testing.T) {
		l := f.newLog(t)
		for range 6 {
			if _, err := l.Append(ctx, "s", streamhub.Event{Data: make([]byte, 10)}); err != nil {
				t.Fatal(err)
			}
		}
		if err := l.Trim(ctx, "s", streamhub.Retention{MaxBytes: 25}); err != nil {
			t.Fatal(err)
		}
		got, err := after(l, "s", 4)
		if err != nil {
			t.Fatalf("After(4): %v", err)
		}
		wantSeqs(t, got, 5, 6)
		if _, err := after(l, "s", 3); !errors.Is(err, streamhub.ErrGap) {
			t.Fatalf("After(3) err = %v, want ErrGap", err)
		}
	})

	t.Run("TrimIsNoOpWithinLimits", func(t *testing.T) {
		l := f.newLog(t)
		appendN(t, l, "s", 3)
		if err := l.Trim(ctx, "s", streamhub.Retention{MaxRecords: 10}); err != nil {
			t.Fatal(err)
		}
		got, err := after(l, "s", 0)
		if err != nil {
			t.Fatal(err)
		}
		wantSeqs(t, got, 1, 3)
	})

	t.Run("PagedReplayLargerThanAPage", func(t *testing.T) {
		l := f.newLog(t)
		const n = 1000
		appendN(t, l, "s", n)
		got, err := after(l, "s", 0)
		if err != nil {
			t.Fatal(err)
		}
		wantSeqs(t, got, 1, n)
		got, err = after(l, "s", 250)
		if err != nil {
			t.Fatal(err)
		}
		wantSeqs(t, got, 251, n)
	})

	t.Run("EarlyBreak", func(t *testing.T) {
		l := f.newLog(t)
		appendN(t, l, "s", 50)
		count := 0
		for _, err := range l.After(ctx, "s", 0) {
			if err != nil {
				t.Fatal(err)
			}
			count++
			if count == 3 {
				break
			}
		}
		if count != 3 {
			t.Fatalf("count = %d", count)
		}
		if _, err := l.Append(ctx, "s", streamhub.Event{}); err != nil {
			t.Fatalf("Append after early break: %v", err)
		}
	})

	t.Run("ConcurrentAppendsAreGapFree", func(t *testing.T) {
		l := f.newLog(t)
		const workers, each = 8, 50
		var wg sync.WaitGroup
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range each {
					if _, err := l.Append(ctx, "s", streamhub.Event{}); err != nil {
						t.Errorf("Append: %v", err)
						return
					}
				}
			}()
		}
		wg.Wait()
		got, err := after(l, "s", 0)
		if err != nil {
			t.Fatal(err)
		}
		wantSeqs(t, got, 1, workers*each)
	})

	t.Run("CanceledContext", func(t *testing.T) {
		l := f.newLog(t)
		appendN(t, l, "s", 2)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := l.Append(cctx, "s", streamhub.Event{}); err == nil {
			t.Fatal("Append with canceled ctx succeeded")
		}
		for _, err := range l.After(cctx, "s", 0) {
			if err == nil {
				t.Fatal("After with canceled ctx yielded a record")
			}
			break
		}
	})

	if f.Reopen != nil {
		t.Run("ReopenPreservesRecordsAndPrunedThrough", func(t *testing.T) {
			l := f.New(t)
			appendN(t, l, "s", 8)
			if err := l.Trim(ctx, "s", streamhub.Retention{MaxRecords: 5}); err != nil {
				t.Fatal(err)
			}
			re := f.Reopen(t, l)
			t.Cleanup(func() { _ = re.Close() })
			h, err := re.Head(ctx, "s")
			if err != nil {
				t.Fatal(err)
			}
			if h.Latest != 8 || h.PrunedThrough != 3 {
				t.Fatalf("head after reopen = %+v", h)
			}
			got, err := after(re, "s", 3)
			if err != nil {
				t.Fatal(err)
			}
			wantSeqs(t, got, 4, 8)
			if _, gerr := after(re, "s", 2); !errors.Is(gerr, streamhub.ErrGap) {
				t.Fatalf("After(2) err = %v, want ErrGap", gerr)
			}
			r, err := re.Append(ctx, "s", streamhub.Event{})
			if err != nil || r.Seq != 9 {
				t.Fatalf("Append after reopen = %+v, %v; want Seq 9", r, err)
			}
		})
	}

	t.Run("CloseRejectsFurtherUse", func(t *testing.T) {
		l := f.New(t)
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := l.Append(ctx, "s", streamhub.Event{}); !errors.Is(err, streamhub.ErrClosed) {
			t.Fatalf("Append after Close = %v, want ErrClosed", err)
		}
		if _, err := l.Head(ctx, "s"); !errors.Is(err, streamhub.ErrClosed) {
			t.Fatalf("Head after Close = %v, want ErrClosed", err)
		}
	})
}
