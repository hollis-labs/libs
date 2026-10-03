// Package main demonstrates iterating a synthetic stream of StreamEvent
// values and using IsTurnComplete to detect terminal events. In real
// usage, the events come from a provider adapter; here they are inline
// so the example runs without network or credentials.
//
// Run:
//
//	go run ./examples/streamevent
package main

import (
	"fmt"
	"strings"

	llmtypes "github.com/hollis-labs/go-llm-types"
)

func main() {
	events := []llmtypes.StreamEvent{
		{Type: llmtypes.EventSessionID, SessionID: "sess-abc123"},
		{Type: llmtypes.EventDelta, Content: "The capital "},
		{Type: llmtypes.EventDelta, Content: "of France "},
		{Type: llmtypes.EventDelta, Content: "is Paris."},
		{Type: llmtypes.EventThinking, ThinkingBlock: &llmtypes.ThinkingBlock{
			Thinking:  "User asked a basic geography question; respond directly.",
			Signature: "sig-xyz",
		}},
		{Type: llmtypes.EventUsage, Usage: &llmtypes.Usage{
			InputTokens:  42,
			OutputTokens: 7,
			StopReason:   "end_turn",
		}},
		{Type: llmtypes.EventDone},
	}

	var text strings.Builder
	for _, ev := range events {
		switch ev.Type {
		case llmtypes.EventSessionID:
			fmt.Printf("[session] %s\n", ev.SessionID)
		case llmtypes.EventDelta:
			text.WriteString(ev.Content)
		case llmtypes.EventThinking:
			if ev.ThinkingBlock != nil {
				fmt.Printf("[thinking] %s\n", ev.ThinkingBlock.Thinking)
			} else {
				fmt.Printf("[thinking] (no block)\n")
			}
		case llmtypes.EventUsage:
			if ev.Usage != nil {
				fmt.Printf("[usage] in=%d out=%d stop=%s\n",
					ev.Usage.InputTokens, ev.Usage.OutputTokens, ev.Usage.StopReason)
			} else {
				fmt.Printf("[usage] (no usage)\n")
			}
		case llmtypes.EventError:
			fmt.Printf("[error] %s\n", ev.Error)
		}

		if llmtypes.IsTurnComplete(ev) {
			fmt.Printf("[done] terminal event: %s\n", ev.Type)
			break
		}
	}

	fmt.Printf("\nAssembled response: %q\n", text.String())
}
