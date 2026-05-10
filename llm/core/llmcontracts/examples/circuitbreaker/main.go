// Package main demonstrates CircuitBreaker, which trips after a configurable
// number of consecutive failures, blocks new requests during a cooldown, then
// allows a single half-open probe before fully closing on success.
//
// Adapters wrap each provider call with the breaker:
//
//	if cb.IsOpen() {
//	    return errCircuitOpen
//	}
//	if err := callProvider(); err != nil {
//	    cb.RecordFailure()
//	    return err
//	}
//	cb.RecordSuccess()
package main

import (
	"errors"
	"fmt"

	llmcontracts "github.com/hollis-labs/go-llm-contracts"
)

var errProvider = errors.New("simulated provider failure")

func callProvider(failTimes int) error {
	if failTimes > 0 {
		return errProvider
	}
	return nil
}

func main() {
	// Trip after 3 consecutive failures.
	cb := llmcontracts.NewCircuitBreaker(3)

	// Simulate a burst of failures.
	for i := 0; i < 4; i++ {
		if cb.IsOpen() {
			fmt.Printf("attempt %d: circuit open, skipping call\n", i+1)
			continue
		}
		err := callProvider(1)
		if err != nil {
			tripped := cb.RecordFailure()
			fmt.Printf("attempt %d: failure (tripped=%v, state=%v)\n", i+1, tripped, cb.State())
		}
	}

	// On the next success, the breaker closes and counters reset.
	cb.Reset()
	if err := callProvider(0); err == nil {
		cb.RecordSuccess()
	}
	fmt.Printf("after recovery: state=%v\n", cb.State())
}
