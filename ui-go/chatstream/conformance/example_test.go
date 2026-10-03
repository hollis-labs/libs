package conformance_test

import (
	"fmt"
	"time"

	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui-go/chatstream/conformance"
)

// Validate reports every broken invariant of a stream. This stream never ends.
func ExampleValidate() {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	events := []chatstream.Event{
		{V: chatstream.SchemaVersion, RunID: "r", Time: at, Verb: chatstream.VerbRunStart},
		{V: chatstream.SchemaVersion, RunID: "r", Time: at, Verb: chatstream.VerbPartStart, PartID: "p", Kind: "text"},
	}
	for _, v := range conformance.Validate(events) {
		fmt.Println(v)
	}
	// Output: terminal: the stream has no terminal event
}
