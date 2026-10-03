package fakeserver_test

import (
	"context"
	"fmt"

	chatstream "github.com/hollis-labs/libs/ui-go/chatstream"
	"github.com/hollis-labs/libs/ui-go/chatstream/conformance/fakeserver"
)

// A provider connection that dies mid-stream must not look like a success. The
// scripted upstream cuts the connection after two frames; the decoder closes the
// run with an error.
func ExampleDecodeOverHTTP() {
	u := fakeserver.DropAfter(echoFrames(), 2).Memory()

	for ev, err := range fakeserver.DecodeOverHTTP(context.Background(), u.URL, echo{},
		chatstream.DecodeOptions{RunID: "run-1"}, fakeserver.WithClient(u.Client)) {
		if err != nil {
			fmt.Println("error:", err)
			return
		}
		if ev.IsTerminal() {
			fmt.Println(ev.Verb, ev.Code, ev.Retryable)
		}
	}
	// Output: run.error upstream_truncated true
}

func ExamplePlan() {
	// The first connection delivers frames 1-2 and dies; the resume is answered
	// 404, which a client must treat as final.
	u := fakeserver.ResumeNotFound(echoFrames(), 2).Memory()
	for range 2 {
		resp, err := u.Client.Get(u.URL)
		if err == nil {
			fmt.Println(resp.StatusCode)
			resp.Body.Close()
		}
	}
	fmt.Printf("%q\n", u.LastEventIDs())
	// Output:
	// 200
	// 404
	// ["" ""]
}
