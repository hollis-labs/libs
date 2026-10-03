package streamhub_test

import (
	"context"
	"errors"
	"testing"

	streamhub "github.com/hollis-labs/libs/ui-go/streamhub"
)

func newTestHub(t *testing.T, o ...streamhub.Option) *streamhub.Hub {
	t.Helper()
	h := streamhub.New(streamhub.NewMemoryLog(), o...)
	t.Cleanup(func() { _ = h.Shutdown(context.Background()) })
	return h
}

func TestPublishSubscribeNext(t *testing.T) {
	ctx := context.Background()
	h := newTestHub(t)
	rec, err := h.Publish(ctx, "s", streamhub.Event{Name: "greeting", Data: []byte("hi")})
	if err != nil || rec.Seq != 1 || rec.At.IsZero() {
		t.Fatalf("Publish = %+v, %v", rec, err)
	}
	sub, err := h.Subscribe(ctx, "s", streamhub.SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	it := mustNext(t, sub)
	if it.Gap != nil || it.Record.Name != "greeting" || string(it.Record.Data) != "hi" {
		t.Fatalf("item = %+v", it)
	}
}

func TestNewPanicsOnNilLog(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New(nil) did not panic")
		}
	}()
	streamhub.New(nil)
}

func TestUnknownStream(t *testing.T) {
	ctx := context.Background()
	h := newTestHub(t, streamhub.WithAutoOpen(false))
	if _, err := h.Publish(ctx, "s", streamhub.Event{}); !errors.Is(err, streamhub.ErrUnknownStream) {
		t.Fatalf("Publish = %v", err)
	}
	if _, err := h.Subscribe(ctx, "s", streamhub.SubscribeOptions{}); !errors.Is(err, streamhub.ErrUnknownStream) {
		t.Fatalf("Subscribe = %v", err)
	}
	if err := h.Close(ctx, "s"); !errors.Is(err, streamhub.ErrUnknownStream) {
		t.Fatalf("Close = %v", err)
	}
	if err := h.Open(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Publish(ctx, "s", streamhub.Event{}); err != nil {
		t.Fatalf("Publish after Open = %v", err)
	}
}

func TestEmptyStreamNameRejected(t *testing.T) {
	ctx := context.Background()
	h := newTestHub(t)
	if _, err := h.Publish(ctx, "", streamhub.Event{}); !errors.Is(err, streamhub.ErrUnknownStream) {
		t.Fatalf("Publish = %v", err)
	}
	if _, err := h.Subscribe(ctx, "", streamhub.SubscribeOptions{}); !errors.Is(err, streamhub.ErrUnknownStream) {
		t.Fatalf("Subscribe = %v", err)
	}
}

func TestShutdown(t *testing.T) {
	ctx := context.Background()
	h := streamhub.New(streamhub.NewMemoryLog())
	sub, err := h.Subscribe(ctx, "s", streamhub.SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Publish(ctx, "s", streamhub.Event{}); err != nil {
		t.Fatal(err)
	}
	if err := h.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	// Records already delivered to the subscriber are still readable.
	if it := mustNext(t, sub); it.Record.Seq != 1 {
		t.Fatalf("item = %+v", it)
	}
	if _, err := sub.Next(ctx); !errors.Is(err, streamhub.ErrClosed) {
		t.Fatalf("Next after Shutdown = %v", err)
	}
	if _, err := h.Publish(ctx, "s", streamhub.Event{}); !errors.Is(err, streamhub.ErrClosed) {
		t.Fatalf("Publish after Shutdown = %v", err)
	}
	if _, err := h.Subscribe(ctx, "s", streamhub.SubscribeOptions{}); !errors.Is(err, streamhub.ErrClosed) {
		t.Fatalf("Subscribe after Shutdown = %v", err)
	}
	if err := h.Shutdown(ctx); err != nil {
		t.Fatalf("second Shutdown = %v", err)
	}
}

func TestNextHonoursContext(t *testing.T) {
	h := newTestHub(t)
	sub, err := h.Subscribe(context.Background(), "s", streamhub.SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sub.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Next = %v", err)
	}
}

// A hub over a populated Log continues its numbering and knows a stream that
// already ended.
func TestHubOverPopulatedLog(t *testing.T) {
	ctx := context.Background()
	l := streamhub.NewMemoryLog()
	for range 3 {
		if _, err := l.Append(ctx, "s", streamhub.Event{Name: "e"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.Append(ctx, "done", streamhub.Event{Name: "end"}); err != nil {
		t.Fatal(err)
	}
	h := streamhub.New(l, streamhub.WithTerminal(func(r streamhub.Record) bool { return r.Name == "end" }))
	defer h.Shutdown(ctx)
	rec, err := h.Publish(ctx, "s", streamhub.Event{})
	if err != nil || rec.Seq != 4 {
		t.Fatalf("Publish = %+v, %v; want seq 4", rec, err)
	}
	if _, err := h.Publish(ctx, "done", streamhub.Event{}); !errors.Is(err, streamhub.ErrTerminated) {
		t.Fatalf("Publish to ended stream = %v", err)
	}
	if err := h.Open(ctx, "done"); !errors.Is(err, streamhub.ErrTerminated) {
		t.Fatalf("Open = %v", err)
	}
}
