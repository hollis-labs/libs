// Package main demonstrates TokenRateTracker, a sliding 60-second window
// helper that lets adapters pace requests against a per-minute token budget.
//
// Typical usage:
//
//  1. Construct with a default per-minute limit.
//  2. Call UpdateLimit when the provider returns an authoritative limit.
//  3. Before each request: WaitTime(estimatedTokens) -> sleep -> Record.
package main

import (
	"context"
	"fmt"
	"time"

	llmcontracts "github.com/hollis-labs/go-llm-contracts"
)

func main() {
	// Default limit of 40,000 input tokens per minute. Adapters typically
	// refine this with UpdateLimit once they see a real response header.
	tracker := llmcontracts.NewTokenRateTracker(40_000)

	// Pretend we already spent 35,000 tokens in the current window.
	tracker.Record(35_000)

	// Pre-flight a 10,000-token request: it does not fit, so WaitTime
	// returns the duration until enough budget frees up.
	const requestTokens = 10_000
	wait := tracker.WaitTime(requestTokens)

	if wait > 0 {
		fmt.Printf("rate budget: waiting %v before sending %d tokens\n", wait.Round(time.Second), requestTokens)

		// PacingWait emits periodic status callbacks so upstream stall
		// watchdogs do not misclassify the wait as a hang.
		_ = llmcontracts.PacingWait(context.Background(), 50*time.Millisecond, func(msg string) {
			fmt.Println("status:", msg)
		})
	}

	// If the request will never fit (larger than the entire window),
	// WaitTime still returns a non-zero duration but callers should also
	// guard with a hard fail using ErrRequestExceedsRateBudget.
	if requestTokens > 40_000 {
		fmt.Println("would fail with:", llmcontracts.ErrRequestExceedsRateBudget)
	}

	avail, limit := tracker.Remaining()
	fmt.Printf("remaining budget: %d / %d tokens\n", avail, limit)
}
