package conformance_test

import (
	"io"
	"testing"

	ssekit "github.com/hollis-labs/go-ssekit"
	"github.com/hollis-labs/go-ssekit/conformance"
)

// parseWithSSEKit adapts ssekit.Read to conformance.ParseFunc; any other
// parser adapts the same way.
func parseWithSSEKit(r io.Reader) ([]ssekit.Event, error) {
	var evs []ssekit.Event
	for ev, err := range ssekit.Read(r) {
		if err != nil {
			return evs, err
		}
		evs = append(evs, ev)
	}
	return evs, nil
}

// TestParserConforms runs the suite as a parser's own test file would.
func TestParserConforms(t *testing.T) {
	conformance.Run(t, parseWithSSEKit)
}

// ExampleRun shows the call a parser's test makes. It is compile-checked only
// (no Output comment): Run needs the *testing.T of a real test, which
// TestParserConforms supplies.
func ExampleRun() {
	var t *testing.T // in real use: func TestMyParser(t *testing.T)
	conformance.Run(t, parseWithSSEKit)
}
