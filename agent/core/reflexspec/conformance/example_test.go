package conformance_test

import (
	"fmt"

	"github.com/hollis-labs/reflex-definition-spec/conformance"
)

func ExampleRun() {
	cases, err := conformance.Load(nil, conformance.Root)
	if err != nil {
		fmt.Println(err)
		return
	}
	failed := 0
	for _, c := range cases {
		if conformance.Run(c) != nil {
			failed++
		}
	}
	fmt.Println("failing cases:", failed)
	// Output: failing cases: 0
}
