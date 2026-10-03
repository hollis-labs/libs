package timing_test

import (
	"fmt"
	"time"

	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui-go/chatstream/conformance"
	"github.com/hollis-labs/libs/ui-go/chatstream/conformance/timing"
)

// A provider that "streams" but holds the text back and delivers it at once looks
// like a burst however long the wait before it; frames arriving over time do not.
func ExampleClassify() {
	frame := chatstream.Frame{Data: []byte("{}")}
	phased := []conformance.TimedFrame{{Delay: 800 * time.Millisecond, Frame: frame}, {Delay: time.Millisecond, Frame: frame}, {Delay: time.Millisecond, Frame: frame}}
	live := []conformance.TimedFrame{{Delay: 300 * time.Millisecond, Frame: frame}, {Delay: 300 * time.Millisecond, Frame: frame}, {Delay: 300 * time.Millisecond, Frame: frame}}

	fmt.Println(timing.Classify(timing.Profile(phased)), timing.Classify(timing.Profile(live)))
	// Output: burst spread
}
