package streamhub_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"testing/synctest"
	"time"

	streamhub "github.com/hollis-labs/libs/ui-go/streamhub"
)

// The grace timers are time.AfterFunc timers created inside the Hub; inside a
// synctest bubble they run on the bubble's virtual clock.
func TestEndedStreamIsRetainedThenForgotten(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		l := streamhub.NewMemoryLog()
		h := streamhub.New(l, streamhub.WithRetainAfterClose(10*time.Second))
		publishN(t, h, "s", 2)
		if err := h.Close(ctx, "s"); err != nil {
			t.Fatal(err)
		}

		time.Sleep(9 * time.Second)
		late := mustSubscribe(t, h, "s", streamhub.SubscribeOptions{})
		if it := mustNext(t, late); it.Record.Seq != 1 {
			t.Fatalf("item = %+v", it)
		}
		if it := mustNext(t, late); it.Record.Seq != 2 {
			t.Fatalf("item = %+v", it)
		}
		if _, err := late.Next(ctx); !errors.Is(err, io.EOF) {
			t.Fatalf("Next = %v, want EOF", err)
		}

		time.Sleep(2 * time.Second) // past the grace period
		synctest.Wait()
		if head, _ := l.Head(ctx, "s"); head.Latest != 0 {
			t.Fatalf("log still holds the stream: %+v", head)
		}
		// The name is free again: a fresh stream starts at 1.
		rec, err := h.Publish(ctx, "s", streamhub.Event{})
		if err != nil || rec.Seq != 1 {
			t.Fatalf("Publish after grace = %+v, %v", rec, err)
		}
		_ = h.Shutdown(ctx)
	})
}

func TestForgottenStreamWithoutAutoOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		h := streamhub.New(streamhub.NewMemoryLog(),
			streamhub.WithAutoOpen(false), streamhub.WithRetainAfterClose(time.Minute))
		if err := h.Open(ctx, "s"); err != nil {
			t.Fatal(err)
		}
		publishN(t, h, "s", 1)
		if err := h.Close(ctx, "s"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute + time.Second)
		synctest.Wait()
		if _, err := h.Subscribe(ctx, "s", streamhub.SubscribeOptions{}); !errors.Is(err, streamhub.ErrUnknownStream) {
			t.Fatalf("Subscribe after grace = %v, want ErrUnknownStream", err)
		}
		_ = h.Shutdown(ctx)
	})
}

func TestRetainAfterCloseZeroAndNegative(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		l := streamhub.NewMemoryLog()
		zero := streamhub.New(l, streamhub.WithRetainAfterClose(0))
		forever := streamhub.New(l, streamhub.WithRetainAfterClose(-1))

		publishN(t, zero, "z", 1)
		if err := zero.Close(ctx, "z"); err != nil {
			t.Fatal(err)
		}
		if head, _ := l.Head(ctx, "z"); head.Latest != 0 {
			t.Fatalf("zero grace kept the stream: %+v", head)
		}

		publishN(t, forever, "f", 1)
		if err := forever.Close(ctx, "f"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(24 * time.Hour)
		synctest.Wait()
		if head, _ := l.Head(ctx, "f"); head.Latest != 1 {
			t.Fatalf("negative grace forgot the stream: %+v", head)
		}
		_ = zero.Shutdown(ctx)
		_ = forever.Shutdown(ctx)
	})
}

func TestTerminalRecordStartsGrace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		l := streamhub.NewMemoryLog()
		h := streamhub.New(l,
			streamhub.WithTerminal(func(r streamhub.Record) bool { return r.Name == "end" }),
			streamhub.WithRetainAfterClose(time.Second))
		if _, err := h.Publish(ctx, "s", streamhub.Event{Name: "end"}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if head, _ := l.Head(ctx, "s"); head.Latest != 0 {
			t.Fatalf("stream survived its grace: %+v", head)
		}
		_ = h.Shutdown(ctx)
	})
}
