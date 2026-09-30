package openaichat_test

import (
	"fmt"

	"github.com/hollis-labs/go-chatstream/adapter/openaichat"
	"github.com/hollis-labs/go-chatstream/conformance"
)

// ExampleNew decodes a recorded stream and prints how the run began and ended:
// bracketed by run.start and exactly one terminal event. A real caller feeds frames from framing.SSE or
// framing.Lines through chatstream.DecodeFrames instead of a fixture.
func ExampleNew() {
	fx, err := conformance.LoadFixture("testdata/text-usage.frames.json")
	if err != nil {
		panic(err)
	}
	events, err := conformance.DecodeFixture(openaichat.New(), fx)
	if err != nil {
		panic(err)
	}
	fmt.Println(events[0].Verb, "...", events[len(events)-1].Verb)
	// Output: run.start ... run.finish
}
