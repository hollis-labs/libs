package hubbind_test

import (
	"context"
	"fmt"
	"time"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/hubbind"
	streamhub "github.com/hollis-labs/go-streamhub"
)

// One hub stream per session. The hub assigns Seq, the terminal event ends the
// stream, and a subscriber that resumes from a cursor gets exactly what followed it.
func Example() {
	ctx := context.Background()
	hub := streamhub.New(streamhub.NewMemoryLog(), streamhub.WithTerminal(hubbind.Terminal))
	defer hub.Shutdown(ctx)

	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, verb := range []chatstream.Verb{chatstream.VerbRunStart, chatstream.VerbRaw, chatstream.VerbRunFinish} {
		ev := chatstream.Event{V: chatstream.SchemaVersion, RunID: "r", Time: at, Verb: verb}
		if verb == chatstream.VerbRaw {
			ev.Raw = &chatstream.Raw{Dialect: "demo", Type: "ping"}
		}
		if _, err := hubbind.Publish(ctx, hub, "session-1", ev); err != nil {
			panic(err)
		}
	}

	sub, err := hub.Subscribe(ctx, "session-1", streamhub.SubscribeOptions{After: 1}) // resume after Seq 1
	if err != nil {
		panic(err)
	}
	defer sub.Close()
	for ev, err := range hubbind.Events(ctx, sub, "r") {
		if err != nil {
			panic(err)
		}
		fmt.Println(ev.Seq, ev.Verb)
	}
	// Output:
	// 2 raw
	// 3 run.finish
}
