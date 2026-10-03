package streamhub_test

import (
	"context"
	"errors"
	"testing"
	"time"

	streamhub "github.com/hollis-labs/libs/ui-go/streamhub"
)

func collect(t *testing.T, l streamhub.Log, stream string, after streamhub.Seq) ([]streamhub.Record, error) {
	t.Helper()
	var out []streamhub.Record
	for r, err := range l.After(context.Background(), stream, after) {
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

func TestMemoryLog_MaxAgeUsesClock(t *testing.T) {
	now := time.Unix(1000, 0)
	l := streamhub.NewMemoryLog(streamhub.WithMemoryClock(func() time.Time { return now }))
	ctx := context.Background()
	for range 3 {
		if _, err := l.Append(ctx, "s", streamhub.Event{Name: "e"}); err != nil {
			t.Fatal(err)
		}
		now = now.Add(10 * time.Second)
	}
	// records are 30s, 20s, 10s old
	if err := l.Trim(ctx, "s", streamhub.Retention{MaxAge: 25 * time.Second}); err != nil {
		t.Fatal(err)
	}
	h, _ := l.Head(ctx, "s")
	if h.PrunedThrough != 1 || h.Latest != 3 || h.Dropped != 1 {
		t.Fatalf("head = %+v", h)
	}
}

func TestMemoryLog_MaxBytes(t *testing.T) {
	l := streamhub.NewMemoryLog()
	ctx := context.Background()
	for range 5 {
		if _, err := l.Append(ctx, "s", streamhub.Event{Data: make([]byte, 10)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Trim(ctx, "s", streamhub.Retention{MaxBytes: 25}); err != nil {
		t.Fatal(err)
	}
	h, _ := l.Head(ctx, "s")
	if h.PrunedThrough != 3 {
		t.Fatalf("PrunedThrough = %d, want 3", h.PrunedThrough)
	}
}

func TestMemoryLog_AutoRetention(t *testing.T) {
	l := streamhub.NewMemoryLog(streamhub.WithMemoryRetention(streamhub.Retention{MaxRecords: 2}))
	ctx := context.Background()
	for range 5 {
		if _, err := l.Append(ctx, "s", streamhub.Event{}); err != nil {
			t.Fatal(err)
		}
	}
	recs, err := collect(t, l, "s", 3)
	if err != nil || len(recs) != 2 || recs[0].Seq != 4 {
		t.Fatalf("recs=%v err=%v", recs, err)
	}
	_, err = collect(t, l, "s", 2)
	var ge *streamhub.GapError
	if !errors.As(err, &ge) || ge.Gap.Missed != 1 || ge.Gap.OldestAvailable != 4 {
		t.Fatalf("err = %v", err)
	}
}

func TestMemoryLog_AppendCopiesData(t *testing.T) {
	l := streamhub.NewMemoryLog()
	buf := []byte("abc")
	if _, err := l.Append(context.Background(), "s", streamhub.Event{Data: buf}); err != nil {
		t.Fatal(err)
	}
	buf[0] = 'X'
	recs, _ := collect(t, l, "s", 0)
	if string(recs[0].Data) != "abc" {
		t.Fatalf("stored data aliased the caller's slice: %q", recs[0].Data)
	}
}

func TestMemoryLog_IterationDoesNotBlockAppend(t *testing.T) {
	l := streamhub.NewMemoryLog(streamhub.WithPageSize(2))
	ctx := context.Background()
	for range 6 {
		if _, err := l.Append(ctx, "s", streamhub.Event{}); err != nil {
			t.Fatal(err)
		}
	}
	first := true
	for r, err := range l.After(ctx, "s", 0) {
		if err != nil {
			t.Fatal(err)
		}
		if first {
			first = false
			// Append from inside the loop body: would deadlock if the
			// iterator held the log's lock across yield.
			if _, err := l.Append(ctx, "s", streamhub.Event{}); err != nil {
				t.Fatal(err)
			}
		}
		_ = r
	}
}

func TestMemoryLog_ForgetAndClose(t *testing.T) {
	l := streamhub.NewMemoryLog()
	ctx := context.Background()
	if _, err := l.Append(ctx, "s", streamhub.Event{}); err != nil {
		t.Fatal(err)
	}
	if err := l.Forget(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	if h, _ := l.Head(ctx, "s"); h != (streamhub.Head{}) {
		t.Fatalf("head after Forget = %+v", h)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append(ctx, "s", streamhub.Event{}); !errors.Is(err, streamhub.ErrClosed) {
		t.Fatalf("Append after Close = %v", err)
	}
}

func TestMemoryLog_CanceledContext(t *testing.T) {
	l := streamhub.NewMemoryLog()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.Append(ctx, "s", streamhub.Event{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Append = %v", err)
	}
}
