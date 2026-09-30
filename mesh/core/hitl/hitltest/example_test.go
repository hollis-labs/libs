package hitltest_test

import (
	"fmt"

	"github.com/hollis-labs/go-hitl/hitltest"
)

func ExampleScenarios() {
	fmt.Println(len(hitltest.Scenarios()) > 10, len(hitltest.Fixtures()) > 50)
	// Output: true true
}
