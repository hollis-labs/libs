package main

import (
	"context"
	"fmt"
	"strings"

	chatstream "github.com/hollis-labs/go-chatstream"
	"github.com/hollis-labs/go-chatstream/adapter/anthropic"
	"github.com/hollis-labs/go-chatstream/framing"
)

// A recorded Anthropic Messages stream that stops before message_stop: the
// connection dropped mid-answer.
const stream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"claude","usage":{"input_tokens":25,"cache_read_input_tokens":100,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello, wor"}}

`

func main() {
	dec := anthropic.New().NewDecoder(chatstream.DecodeOptions{})
	var events []chatstream.Event
	for ev, err := range chatstream.DecodeFrames(context.Background(), dec, framing.SSE(strings.NewReader(stream))) {
		if err != nil {
			panic(err)
		}
		events = append(events, ev)
	}

	// Truncation is never success: the stream ends with exactly one run.error.
	last := events[len(events)-1]
	fmt.Println(last.Verb, last.Code, last.Retryable)

	msg, err := chatstream.Reduce(events, nil)
	if err != nil {
		panic(err)
	}
	fmt.Printf("%s %q, %d tokens\n", msg.Status, msg.Text(), msg.Usage.Total())
}
