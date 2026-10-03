package capabilities_test

import (
	"fmt"

	"github.com/hollis-labs/substrate/llm-core/contracts/capabilities"
)

func ExampleCheck() {
	host := capabilities.Set{capabilities.Identity, capabilities.MCP}
	agentRequires := capabilities.Set{capabilities.MCP, capabilities.Sandbox}

	fmt.Println(capabilities.Check(host, agentRequires))
	// Output: [sandbox]
}
