package nanitelegacy_test

import (
	"fmt"
	"strings"
	"time"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/sink/nanitelegacy"
	"github.com/hollis-labs/go-chatstream/sink/sinktest"
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

	enc := nanitelegacy.New()
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
	// event: stream_start
	// data: {"event_id":1,"message_id":"r","type":"stream_start"}
	//
	// id: 3
	// event: delta
	// data: {"content":"Hi","event_id":3,"type":"delta"}
	//
	// id: 5
	// event: stream_end
	// data: {"event_id":5,"message_id":"r","type":"stream_end","usage":{"input_tokens":0,"output_tokens":0,"stop_reason":"end_turn"}}
}
