package chatstream_test

import (
	"fmt"
	"time"

	chatstream "github.com/hollis-labs/go-chatstream"
)

// OpenAI's prompt_tokens includes cached tokens and its completion_tokens
// includes reasoning tokens. Converting once, at the decoder, gives disjoint
// components, so Total is what was billed and nothing is counted twice.
func ExampleUsageFromInclusive() {
	u, err := chatstream.UsageFromInclusive(chatstream.UsageFinal, 1000, 600, 0, 200, 50)
	if err != nil {
		panic(err)
	}
	fmt.Println(u.UncachedInput, u.CacheRead, u.Output, u.Reasoning, u.Total())
	// Output: 400 600 150 50 1200
}

// Reduce folds events into a Message. Applying the rest of a stream to the
// snapshot at any cursor gives the same message as applying all of it.
func ExampleReduce() {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ev := func(verb chatstream.Verb, f func(*chatstream.Event)) chatstream.Event {
		e := chatstream.Event{V: chatstream.SchemaVersion, RunID: "r", Time: at, Verb: verb}
		f(&e)
		return e
	}
	events := []chatstream.Event{
		ev(chatstream.VerbRunStart, func(*chatstream.Event) {}),
		ev(chatstream.VerbPartStart, func(e *chatstream.Event) { e.PartID, e.Kind = "p", string(chatstream.PartText) }),
		ev(chatstream.VerbPartDelta, func(e *chatstream.Event) { e.PartID, e.Text = "p", "Hello, " }),
		ev(chatstream.VerbPartDelta, func(e *chatstream.Event) { e.PartID, e.Text = "p", "world" }),
		ev(chatstream.VerbPartEnd, func(e *chatstream.Event) { e.PartID = "p" }),
		ev(chatstream.VerbRunFinish, func(e *chatstream.Event) { e.Reason = string(chatstream.FinishStop) }),
	}

	snapshot, _ := chatstream.Reduce(events[:3], nil) // a client that stopped after event 3
	msg, err := chatstream.Reduce(events[3:], snapshot)
	if err != nil {
		panic(err)
	}
	fmt.Printf("%s %q\n", msg.Status, msg.Text())
	// Output: finished "Hello, world"
}
