package native_test

import (
	"fmt"
	"strings"
	"time"

	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui-go/chatstream/sink/native"
	"github.com/hollis-labs/libs/ui-go/chatstream/sink/sinktest"
)

// ExampleNew encodes a tiny run into the target's wire format. A real caller
// passes the Writer that sink.Start returns for its http.ResponseWriter.
func ExampleNew() {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ev := func(seq uint64, verb chatstream.Verb, f func(*chatstream.Event)) chatstream.Event {
		e := chatstream.Event{V: chatstream.SchemaVersion, Seq: seq, RunID: "r", Time: at, Verb: verb}
		f(&e)
		return e
	}
	events := []chatstream.Event{
		ev(1, chatstream.VerbRunStart, func(*chatstream.Event) {}),
		ev(2, chatstream.VerbPartStart, func(e *chatstream.Event) { e.PartID, e.Kind = "p", string(chatstream.PartText) }),
		ev(3, chatstream.VerbPartDelta, func(e *chatstream.Event) { e.PartID, e.Text = "p", "Hi" }),
		ev(4, chatstream.VerbPartEnd, func(e *chatstream.Event) { e.PartID = "p" }),
		ev(5, chatstream.VerbRunFinish, func(e *chatstream.Event) { e.Reason = string(chatstream.FinishStop) }),
	}

	enc := native.New()
	var out sinktest.Recorder
	for _, e := range events {
		if err := enc.Encode(&out, e); err != nil {
			panic(err)
		}
	}
	if err := enc.Close(&out, nil); err != nil {
		panic(err)
	}
	fmt.Println(strings.TrimSpace(out.String()))
	// Output:
	// id: 1
	// event: run.start
	// data: {"v":"1","seq":1,"run_id":"r","time":"2026-01-01T00:00:00Z","verb":"run.start"}
	//
	// id: 2
	// event: part.start
	// data: {"v":"1","seq":2,"run_id":"r","time":"2026-01-01T00:00:00Z","verb":"part.start","part_id":"p","kind":"text"}
	//
	// id: 3
	// event: part.delta
	// data: {"v":"1","seq":3,"run_id":"r","time":"2026-01-01T00:00:00Z","verb":"part.delta","part_id":"p","text":"Hi"}
	//
	// id: 4
	// event: part.end
	// data: {"v":"1","seq":4,"run_id":"r","time":"2026-01-01T00:00:00Z","verb":"part.end","part_id":"p"}
	//
	// id: 5
	// event: run.finish
	// data: {"v":"1","seq":5,"run_id":"r","time":"2026-01-01T00:00:00Z","verb":"run.finish","reason":"stop"}
}
