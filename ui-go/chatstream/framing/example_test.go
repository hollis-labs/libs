package framing_test

import (
	"fmt"
	"strings"

	"github.com/hollis-labs/go-chatstream/framing"
)

// SSE yields one frame per event; the last, unterminated event is discarded as
// the specification requires.
func ExampleSSE() {
	in := "event: delta\nid: 7\ndata: {\"t\":\"a\"}\n\ndata: [DONE]\n\ndata: cut off"
	for f, err := range framing.SSE(strings.NewReader(in)) {
		if err != nil {
			panic(err)
		}
		fmt.Printf("%q %q %s\n", f.Event, f.ID, f.Data)
	}
	// Output:
	// "delta" "7" {"t":"a"}
	// "" "7" [DONE]
}

// Lines frames newline-delimited JSON (Claude and Codex CLIs, ACP over stdio).
func ExampleLines() {
	for f, err := range framing.Lines(strings.NewReader("{\"a\":1}\n\n{\"b\":2}"), 0) {
		if err != nil {
			panic(err)
		}
		fmt.Println(string(f.Data))
	}
	// Output:
	// {"a":1}
	// {"b":2}
}
