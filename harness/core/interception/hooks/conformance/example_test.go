package conformance_test

import (
	"fmt"

	"github.com/hollis-labs/substrate/harness/interception/hooks/conformance"
)

func ExampleLoad() {
	cases, err := conformance.Load(conformance.FixtureFS, conformance.Root)
	if err != nil {
		panic(err)
	}
	for _, c := range cases {
		if c.Dir == "testdata/PreToolUse/deny-rm" {
			fmt.Println(c.Event, c.Want.ExitCode, c.Want.Output.Decision)
		}
	}
	// Output: PreToolUse 2 deny
}
