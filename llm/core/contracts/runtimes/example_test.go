package runtimes_test

import (
	"fmt"

	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
)

func ExampleMode_Valid() {
	fmt.Println(runtimes.ModeJSONRPCStdio.Valid())
	fmt.Println(runtimes.Mode("app-server").Valid())
	// Output:
	// true
	// false
}

func ExampleIDs() {
	for _, id := range runtimes.IDs() {
		fmt.Println(id)
	}
	// Output:
	// claude
	// codex
	// opencode
	// copilot
	// pi
	// antigravity
}
