package agentcontracts_test

import (
	"fmt"

	agentcontracts "github.com/hollis-labs/agent-contracts-leaf"
)

func ExampleHello() {
	fmt.Println(agentcontracts.Hello())
	// Output: hello from agentcontracts
}
