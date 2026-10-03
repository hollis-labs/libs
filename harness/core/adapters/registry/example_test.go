package registry_test

import (
	"fmt"

	"github.com/hollis-labs/substrate/harness/adapters/registry"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
)

func ExampleLookup() {
	d, _ := registry.Lookup("claude-code")
	fmt.Println(d.ID, d.Binary, d.EnvOverride, d.DefaultMode)
	fmt.Println(d.Has(runtimes.ModeStreamingStdio, runtimes.CapResume))
	// Output:
	// claude claude CLAUDE_CLI_PATH streaming-stdio
	// true
}

func ExampleAll() {
	for _, d := range registry.All() {
		fmt.Println(d.ID, d.DefaultMode, d.HasLayout())
	}
	// Output:
	// claude streaming-stdio true
	// codex jsonrpc-stdio true
	// opencode subprocess-per-turn true
	// copilot acp-stdio false
	// pi acp-stdio false
	// antigravity subprocess-per-turn true
}
