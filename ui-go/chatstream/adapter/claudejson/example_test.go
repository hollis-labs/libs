package claudejson_test

import (
	"fmt"

	"github.com/hollis-labs/libs/ui-go/chatstream/adapter/claudejson"
	"github.com/hollis-labs/libs/ui-go/chatstream/conformance"
)

// ExampleNew decodes a recorded stream and prints how the run began and ended:
// bracketed by run.start and exactly one terminal event. A real caller feeds frames from framing.SSE or
// framing.Lines through chatstream.DecodeFrames instead of a fixture.
func ExampleNew() {
	fx, err := conformance.LoadFixture("testdata/whole_messages.frames.json")
	if err != nil {
		panic(err)
	}
	events, err := conformance.DecodeFixture(claudejson.New(), fx)
	if err != nil {
		panic(err)
	}
	fmt.Println(events[0].Verb, "...", events[len(events)-1].Verb)
	// Output: run.start ... run.finish
}
