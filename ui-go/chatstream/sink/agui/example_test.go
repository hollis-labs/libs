package agui_test

import (
	"fmt"
	"strings"
	"time"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/sink/agui"
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

	enc := agui.New()
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
	// data: {"runId":"r","threadId":"r","timestamp":1767225600000,"type":"RUN_STARTED"}
	//
	// id: 2
	// data: {"messageId":"p","role":"assistant","timestamp":1767225600000,"type":"TEXT_MESSAGE_START"}
	//
	// id: 3
	// data: {"delta":"Hi","messageId":"p","timestamp":1767225600000,"type":"TEXT_MESSAGE_CONTENT"}
	//
	// id: 4
	// data: {"messageId":"p","timestamp":1767225600000,"type":"TEXT_MESSAGE_END"}
	//
	// id: 5
	// data: {"runId":"r","threadId":"r","timestamp":1767225600000,"type":"RUN_FINISHED"}
}
