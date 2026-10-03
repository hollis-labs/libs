package streamhub_test

import (
	"context"
	"testing"
	"time"

	streamhub "github.com/hollis-labs/libs/ui-go/streamhub"
)

// mustNext reads one item. The timeout is a failure net only; tests that
// depend on time run inside a synctest bubble and use virtual time.
func mustNext(t testing.TB, sub streamhub.Subscription) streamhub.Item {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	it, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return it
}

func publishN(t testing.TB, h *streamhub.Hub, stream string, n int) {
	t.Helper()
	for range n {
		if _, err := h.Publish(context.Background(), stream, streamhub.Event{Name: "e"}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
}

func mustSubscribe(t testing.TB, h *streamhub.Hub, stream string, o streamhub.SubscribeOptions) streamhub.Subscription {
	t.Helper()
	sub, err := h.Subscribe(context.Background(), stream, o)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	return sub
}
