package sink_test

import (
	"fmt"

	"github.com/hollis-labs/go-chatstream/sink"
)

// CauseText renders why a stream ended for the error frame an encoder writes.
func ExampleCauseText() {
	fmt.Println(sink.CauseText(nil) == "", sink.CauseText(fmt.Errorf("connection reset")))
	// Output: false the stream ended before the run finished: connection reset
}
