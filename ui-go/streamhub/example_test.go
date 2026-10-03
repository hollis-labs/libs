package streamhub_test

import (
	"context"
	"fmt"

	streamhub "github.com/hollis-labs/libs/ui-go/streamhub"
)

func Example() {
	ctx := context.Background()
	hub := streamhub.New(streamhub.NewMemoryLog())
	defer hub.Shutdown(ctx)

	hub.Publish(ctx, "session-1", streamhub.Event{Name: "delta", Data: []byte("hello")})
	hub.Publish(ctx, "session-1", streamhub.Event{Name: "delta", Data: []byte("world")})

	// Replay everything after cursor 1, then keep listening for live records.
	sub, err := hub.Subscribe(ctx, "session-1", streamhub.SubscribeOptions{After: 1})
	if err != nil {
		panic(err)
	}
	defer sub.Close()

	item, err := sub.Next(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println(item.Record.Seq, item.Record.Name, string(item.Record.Data))
	// Output: 2 delta world
}

func ExampleSubscribeOptions_resume() {
	ctx := context.Background()
	hub := streamhub.New(streamhub.NewMemoryLog())
	defer hub.Shutdown(ctx)
	for range 5 {
		hub.Publish(ctx, "s", streamhub.Event{Name: "e"})
	}

	// A subscriber whose buffer overflows is closed, not silently thinned.
	sub, _ := hub.Subscribe(ctx, "s", streamhub.SubscribeOptions{After: streamhub.FromLatest, Buffer: 2})
	for range 5 {
		hub.Publish(ctx, "s", streamhub.Event{Name: "e"})
	}
	var last streamhub.Seq
	for {
		item, err := sub.Next(ctx)
		if err != nil {
			fmt.Println("closed:", err)
			break
		}
		last = item.Record.Seq
	}

	// It resumes from the cursor in the error and misses nothing.
	resumed, _ := hub.Subscribe(ctx, "s", streamhub.SubscribeOptions{After: last})
	defer resumed.Close()
	item, _ := resumed.Next(ctx)
	fmt.Println("resumed at", item.Record.Seq)
	// Output:
	// closed: streamhub: slow consumer closed; resume after seq 7
	// resumed at 8
}
